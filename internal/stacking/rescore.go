package stacking

import (
	"context"
	"log/slog"
	"math"
	"path"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// scoreMethod is how subs are scored (package quality). 1: the weight takes
// the square of the sub's transparency, so hazy subs score low.
//
// A sub is scored once, when it is classified; one in a master is not looked
// at again. Masters stacked under an older method have their subs scored
// again (rescoreAdded): those now below the cut leave, and the rest take
// their new weights. Subs left out for a low score are classified again every
// RetryAfter anyway, so those now above it come back in on their own.
const scoreMethod = 1

// rescoreWeightTolerance is how far a sub's weight may move without its
// master being restacked for it. Clear subs come out at a transparency of
// 0.9 to 1, a weight 0.8 to 1 of the old, which changes a master's signal
// to noise by about 1%; a rebuild reads every sub two or three times.
const rescoreWeightTolerance = 0.2

// rescoreAdded scores the subs of masters stacked under an older scoreMethod
// again.
func (p *Pipeline) rescoreAdded(ctx context.Context) {
	var n int64
	if err := p.db.WithContext(ctx).Model(&app.Stack{}).
		Where("score_method IS NULL OR score_method < ?", scoreMethod).Count(&n).Error; err != nil || n == 0 {
		if err != nil && ctx.Err() == nil {
			slog.Error("Finding masters to score again failed", "error", err)
		}
		return
	}
	measured, err := p.measuredSubs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Scoring masters' subs again failed", "error", err)
		}
		return
	}
	scores, err := p.scorer.Load(ctx, p.sched, p.opts.Pedestal, measured)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Scoring masters' subs again failed", "error", err)
		}
		return
	}
	if err := p.rescore(ctx, scores, time.Now()); err != nil && ctx.Err() == nil {
		slog.Error("Scoring masters' subs again failed", "error", err)
	}
}

// rescore applies scores to the added subs of every master with an older
// scoreMethod, as classify would: a sub below the cut is taken out (low
// score, looked at again after RetryAfter), the others get the new score and
// weight. A master that lost a sub, or whose weights moved by more than
// rescoreWeightTolerance, is marked for the moon sweep to restack. Subs with
// no score now, or rejected in Target Scheduler since, are left as they are,
// as before.
func (p *Pipeline) rescore(ctx context.Context, scores map[string]quality.SubScore, now time.Time) error {
	var stacks []app.Stack
	if err := p.db.WithContext(ctx).Where("score_method IS NULL OR score_method < ?", scoreMethod).
		Order("id").Find(&stacks).Error; err != nil {
		return err
	}
	var left, reweighted, restack int
	for _, stack := range stacks {
		if p.stopping(ctx) {
			return ctx.Err()
		}
		marked, out, moved, err := p.rescoreStack(ctx, stack, scores, now)
		if err != nil {
			return err
		}
		left += out
		reweighted += moved
		if marked {
			restack++
		}
	}
	if len(stacks) > 0 {
		slog.Info("Scored masters' subs again", "method", scoreMethod, "masters", len(stacks),
			"subs_out", left, "subs_reweighted", reweighted, "masters_to_restack", restack)
	}
	return nil
}

// rescoreStack is rescore for one master. It waits for the master's target
// to be free: a batch stacking it saves the whole stack row when it is done,
// which would undo the mark.
func (p *Pipeline) rescoreStack(ctx context.Context, stack app.Stack, scores map[string]quality.SubScore, now time.Time) (marked bool, left, reweighted int, err error) {
	for !p.hold(stack.Object) {
		select {
		case <-ctx.Done():
			return false, 0, 0, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	defer p.release(stack.Object)
	var rows []struct {
		app.StackFrame
		Key string
	}
	if err := p.db.WithContext(ctx).Table("stack_frames").
		Select("stack_frames.*, frames.key").
		Joins("JOIN frames ON frames.id = stack_frames.frame_id").
		Where("stack_frames.stack_id = ? AND stack_frames.status = ?", stack.ID, app.StackStatusAdded).
		Scan(&rows).Error; err != nil {
		return false, 0, 0, err
	}
	next := now.Add(p.opts.RetryAfter)
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, r := range rows {
			s, ok := scores[path.Base(r.Key)]
			if !ok || s.GradingStatus == quality.GradingRejected {
				continue
			}
			if !(s.Score > 0) || s.Score < p.opts.MinScore*s.TargetBest {
				if err := tx.Model(&app.StackFrame{}).Where("id = ?", r.ID).UpdateColumns(map[string]any{
					"status": app.StackStatusLowScore, "score": s.Score, "processed_at": now, "next_attempt_at": next,
				}).Error; err != nil {
					return err
				}
				left++
				marked = true
				continue
			}
			weight := s.Score * r.Exposure
			if weight == r.Weight && s.Score == r.Score {
				continue
			}
			if err := tx.Model(&app.StackFrame{}).Where("id = ?", r.ID).
				UpdateColumns(map[string]any{"score": s.Score, "weight": weight}).Error; err != nil {
				return err
			}
			if !(r.Weight > 0) || math.Abs(weight/r.Weight-1) > rescoreWeightTolerance {
				reweighted++
				marked = true
			}
		}
		cols := map[string]any{"score_method": scoreMethod}
		if marked {
			cols["needs_rebuild"] = true
		}
		// Marked with its subs, so a restart in between still stacks it.
		return tx.Model(&app.Stack{}).Where("id = ?", stack.ID).UpdateColumns(cols).Error
	})
	if err != nil {
		return false, 0, 0, err
	}
	return marked, left, reweighted, nil
}
