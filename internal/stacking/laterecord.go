package stacking

import (
	"context"
	"log/slog"
	"math"
	"path"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// reconcileLateRecords applies Target Scheduler records that arrived after
// their lights were scored from their header and pixels (see reconcileLate).
func (p *Pipeline) reconcileLateRecords(ctx context.Context) {
	var n int64
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("metadata_source = ?", app.MetadataSourceHeader).Count(&n).Error; err != nil || n == 0 {
		if err != nil && ctx.Err() == nil {
			slog.Error("Finding lights scored from their header failed", "error", err)
		}
		return
	}
	measured, err := p.measuredSubs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Applying late Target Scheduler records failed", "error", err)
		}
		return
	}
	scores, err := p.loadScores(ctx, measured)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Applying late Target Scheduler records failed", "error", err)
		}
		return
	}
	if err := p.reconcileLate(ctx, scores, time.Now()); err != nil && ctx.Err() == nil {
		slog.Error("Applying late Target Scheduler records failed", "error", err)
	}
}

type lateRow struct {
	app.StackFrame
	Key    string
	Object string
	Filter string
}

// reconcileLate moves the lights scored from their header whose Target
// Scheduler record has since arrived over to it. One in a master takes the
// record's score and weight, or leaves the master when the record rejects
// it or scores it under the cut, and its master is marked for the moon
// sweep to restack when it lost a sub or its weight moved by more than
// rescoreWeightTolerance. One left out is classified again at once. Either
// way its MetadataSource becomes app.MetadataSourceTS, so it is done once.
func (p *Pipeline) reconcileLate(ctx context.Context, scores map[string]quality.SubScore, now time.Time) error {
	var rows []lateRow
	if err := p.db.WithContext(ctx).Table("stack_frames").
		Select("stack_frames.*, frames.key, frames.object, frames.filter").
		Joins("JOIN frames ON frames.id = stack_frames.frame_id").
		Where("stack_frames.metadata_source = ?", app.MetadataSourceHeader).
		Order("stack_frames.id").Scan(&rows).Error; err != nil {
		return err
	}
	byObject := map[string][]lateRow{}
	var objects []string
	for _, r := range rows {
		s, ok := scores[path.Base(r.Key)]
		if !ok || s.Measured {
			continue
		}
		if _, seen := byObject[r.Object]; !seen {
			objects = append(objects, r.Object)
		}
		byObject[r.Object] = append(byObject[r.Object], r)
	}
	slices.Sort(objects)
	var subs, restack int
	for _, object := range objects {
		if p.stopping(ctx) {
			return ctx.Err()
		}
		marked, err := p.reconcileObject(ctx, object, byObject[object], scores, now)
		if err != nil {
			return err
		}
		subs += len(byObject[object])
		restack += marked
	}
	if subs > 0 {
		slog.Info("Applied Target Scheduler records that arrived after their lights were scored from the header",
			"subs", subs, "masters_to_restack", restack)
	}
	return nil
}

// reconcileObject is reconcileLate for one target's lights, holding the
// target as rescoreStack does. It returns how many masters it marked.
func (p *Pipeline) reconcileObject(ctx context.Context, object string, rows []lateRow, scores map[string]quality.SubScore, now time.Time) (int, error) {
	for !p.hold(object) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	defer p.release(object)
	marked := map[int]bool{}
	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.FrameID
	}
	rims, err := p.rimStreaks(ctx, ids)
	if err != nil {
		return 0, err
	}
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, r := range rows {
			rim, has := rims[r.FrameID]
			cols, restack := p.lateColumns(r, scores[path.Base(r.Key)], rim, has, now)
			if err := tx.Model(&app.StackFrame{}).Where("id = ?", r.ID).UpdateColumns(cols).Error; err != nil {
				return err
			}
			if restack && r.StackID != nil {
				marked[*r.StackID] = true
			}
		}
		if len(marked) == 0 {
			return nil
		}
		ids := make([]int, 0, len(marked))
		for id := range marked {
			ids = append(ids, id)
		}
		return tx.Model(&app.Stack{}).Where("id IN ?", ids).UpdateColumn("needs_rebuild", true).Error
	})
	if err != nil {
		return 0, err
	}
	return len(marked), nil
}

// lateColumns is the update reconcileLate makes to one light given its
// record's score s, and whether its master needs restacking for it.
func (p *Pipeline) lateColumns(r lateRow, s quality.SubScore, rim app.RimStreak, hasRim bool, now time.Time) (map[string]any, bool) {
	if r.Status == app.StackStatusNight {
		r.Status = app.StackStatusAdded
		cols, _ := p.lateColumns(r, s, rim, hasRim, now)
		return cols, false
	}
	cols := map[string]any{"metadata_source": app.MetadataSourceTS}
	if r.Status != app.StackStatusAdded {
		if slices.Contains(retried(), r.Status) {
			cols[columnNextAttemptAt] = now
		}
		return cols, false
	}
	switch {
	case s.GradingStatus == quality.GradingRejected && !s.StackerRejected:
		cols[columnStatus], cols[columnScore], cols[columnProcessedAt], cols[columnNextAttemptAt] = app.StackStatusRejected, s.Score, now, nil
		return cols, true
	case p.lowScore(s):
		cols[columnStatus], cols[columnScore], cols[columnProcessedAt] = app.StackStatusLowScore, s.Score, now
		cols[columnNextAttemptAt], cols[columnError] = now.Add(p.opts.RetryAfter), p.lowScoreReason(s, r.Filter)
		return cols, true
	}
	weight, out, why := rimWeight(s.Score, r.Exposure, rim, hasRim)
	if out {
		cols[columnStatus], cols[columnScore], cols[columnProcessedAt] = app.StackStatusLowScore, s.Score, now
		cols[columnNextAttemptAt], cols[columnError] = now.Add(p.opts.RetryAfter), why
		return cols, true
	}
	cols[columnScore], cols["weight"], cols[columnError] = s.Score, weight, optional(why)
	return cols, !(r.Weight > 0) || math.Abs(weight/r.Weight-1) > rescoreWeightTolerance
}
