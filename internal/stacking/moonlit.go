package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/indexer"
	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/minio/minio-go/v7"
	"gorm.io/gorm"
)

// Moon avoidance after the fact: a light Target Scheduler's exposure
// templates wouldn't have allowed, had it been shot under them, is left out
// of its master like one they did keep out. A master with nothing but
// moonlit lights keeps them, as the low-score cutoff keeps a panel imaged
// only under the moon, rather than leave a hole; once a moon-free light
// comes, the moonlit ones go.

// moonRule is one filter's moon avoidance, from its exposure templates.
type moonRule struct {
	// down: the Moon must be below minAlt for the whole exposure.
	down   bool
	minAlt float64
	// Otherwise the Moon must keep the Lorentzian separation, distance
	// degrees at full moon over width days, unless relaxing (relax > 0)
	// and it is below minAlt.
	distance, width, relax float64
}

// moonRules reads each filter's moon avoidance from Target Scheduler's
// exposure templates. A filter with several templates takes the strictest.
func moonRules(ctx context.Context, sched *gorm.DB) (map[string]moonRule, error) {
	var rows []struct {
		Filtername              string
		Moonavoidanceseparation float64
		Moonavoidancewidth      float64
		Moonrelaxscale          float64
		Moonrelaxminaltitude    float64
		Moondownenabled         int
	}
	if err := sched.WithContext(ctx).Table("exposuretemplate").
		Select("filtername, moonavoidanceseparation, moonavoidancewidth, moonrelaxscale, moonrelaxminaltitude, moondownenabled").
		Where("moonavoidanceenabled = 1").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load moon avoidance: %w", err)
	}
	out := map[string]moonRule{}
	for _, r := range rows {
		cur, seen := out[r.Filtername]
		next := moonRule{down: r.Moondownenabled == 1, minAlt: r.Moonrelaxminaltitude,
			distance: r.Moonavoidanceseparation, width: r.Moonavoidancewidth, relax: r.Moonrelaxscale}
		switch {
		case !seen, next.down && !cur.down:
			out[r.Filtername] = next
		case next.down == cur.down && next.distance > cur.distance:
			out[r.Filtername] = next
		}
	}
	return out, nil
}

// broken reports whether an exposure from start for seconds, pointed at ra,
// dec from lat, lon, breaks the rule.
func (r moonRule) broken(start time.Time, seconds, ra, dec, lat, lon float64) bool {
	end := start.Add(time.Duration(seconds * float64(time.Second)))
	if r.down {
		for _, t := range []time.Time{start, end} {
			if moon.At(t).Altitude(t, lat, lon) > r.minAlt {
				return true
			}
		}
		return false
	}
	mid := start.Add(end.Sub(start) / 2)
	m := moon.At(mid)
	if r.relax > 0 && m.Altitude(mid, lat, lon) <= r.minAlt {
		return false
	}
	return m.Separation(ra, dec) < m.Avoidance(r.distance, r.width)
}

// moonChecker decides whether lights break their filter's moon avoidance.
type moonChecker struct {
	p         *Pipeline
	rules     map[string]moonRule
	positions map[string][2]float64
}

func (p *Pipeline) moonChecker(ctx context.Context, positions map[string][2]float64) (*moonChecker, error) {
	rules, err := moonRules(ctx, p.sched)
	if err != nil {
		return nil, err
	}
	return &moonChecker{p: p, rules: rules, positions: positions}, nil
}

// moonlit reports whether f breaks its filter's rule. A light it can't place
// (no time, pointing or site) is let through.
func (c *moonChecker) moonlit(ctx context.Context, f app.Frame) bool {
	r, ok := c.rules[f.Filter]
	if !ok || f.DateObs == nil || f.Exposure == nil {
		return false
	}
	pos, ok := c.positions[f.Object]
	if !ok {
		if f.MountRA == nil || f.MountDec == nil {
			return false
		}
		pos = [2]float64{*f.MountRA, *f.MountDec}
	}
	site, ok := c.p.site(ctx, f)
	if !ok {
		return false
	}
	return r.broken(*f.DateObs, *f.Exposure, pos[0], pos[1], site[0], site[1])
}

func (p *Pipeline) site(ctx context.Context, f app.Frame) ([2]float64, bool) {
	if v, ok := p.sites.Load(f.Object); ok {
		s, ok := v.([2]float64)
		return s, ok
	}
	kw, err := indexer.ReadHeader(ctx, p.s3, p.source, minio.ObjectInfo{Key: f.Key, Size: f.Size})
	if err != nil {
		slog.Warn("No site for moon avoidance", "object", f.Object, "error", err)
		return [2]float64{}, false // not cached: the read may work next time
	}
	lat, lon := kw.Float("SITELAT"), kw.Float("SITELONG")
	if math.IsNaN(lat) || math.IsNaN(lon) {
		p.sites.Store(f.Object, nil)
		return [2]float64{}, false
	}
	s := [2]float64{lat, lon}
	p.sites.Store(f.Object, s)
	return s, true
}

// moonSweep takes lights already in masters that break moon avoidance out
// of them, and stacks those masters again.
func (p *Pipeline) moonSweep(ctx context.Context) error {
	positions, err := targetPositions(ctx, p.sched)
	if err != nil {
		return err
	}
	check, err := p.moonChecker(ctx, positions)
	if err != nil {
		return err
	}
	ids, stacks, err := p.moonlitAdded(ctx, check)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		ids2 := make([]int, 0, len(stacks))
		for id := range stacks {
			ids2 = append(ids2, id)
		}
		// Marked together, so a restart between taking the lights out
		// and stacking their masters again still stacks them.
		next := time.Now().Add(p.opts.RetryAfter)
		if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&app.StackFrame{}).Where("id IN ?", ids).
				UpdateColumns(map[string]any{columnStatus: app.StackStatusMoon, columnNextAttemptAt: next}).Error; err != nil {
				return err
			}
			return tx.Model(&app.Stack{}).Where("id IN ?", ids2).UpdateColumn("needs_rebuild", true).Error
		}); err != nil {
			return err
		}
		slog.Info("Taking moonlit lights out of their masters", "lights", len(ids), "masters", len(stacks))
	}
	// Masters due a rebuild with nothing on its way in: those left by this
	// sweep or a restart. A master waiting for lights calibrated again is
	// rebuilt when they are added.
	// Also a master older than the moment a light of it was found
	// moonlit (next_attempt_at less RetryAfter), for sweeps from before
	// the masters were marked.
	var due []int
	if err := p.db.WithContext(ctx).Model(&app.Stack{}).
		Where("NOT EXISTS (SELECT 1 FROM stack_frames sf WHERE sf.stack_id = stacks.id AND sf.status = ?)", app.StackStatusRecalibrate).
		Where("needs_rebuild OR EXISTS (SELECT 1 FROM stack_frames sf WHERE sf.stack_id = stacks.id AND sf.status = ? "+
			"AND sf.next_attempt_at - make_interval(secs => ?) > stacks.updated_at)",
			app.StackStatusMoon, p.opts.RetryAfter.Seconds()).
		Pluck("id", &due).Error; err != nil {
		return err
	}
	for _, id := range due {
		if p.stopping(ctx) {
			return nil
		}
		if err := p.restackWithout(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("Restacking without moonlit lights failed", "stack", id, "error", err)
		}
	}
	return nil
}

// moonlitAdded finds the lights in masters that break moon avoidance: their
// stack_frames rows and masters.
func (p *Pipeline) moonlitAdded(ctx context.Context, check *moonChecker) ([]int, map[int]bool, error) {
	var rows []struct {
		SfID    int `gorm:"column:sf_id"`
		SfStack int `gorm:"column:sf_stack"`
		app.Frame
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("f.*, sf.id AS sf_id, sf.stack_id AS sf_stack").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ?", app.StackStatusAdded).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	byStack := map[int][]int{}
	added := map[int]int{}
	for _, r := range rows {
		added[r.SfStack]++
		if check.moonlit(ctx, r.Frame) {
			byStack[r.SfStack] = append(byStack[r.SfStack], r.SfID)
		}
	}
	var ids []int
	stacks := map[int]bool{}
	for stack, moonlit := range byStack {
		if len(moonlit) == added[stack] {
			continue // moon-only: kept
		}
		ids = append(ids, moonlit...)
		stacks[stack] = true
	}
	return ids, stacks, nil
}

// hasMoonFree reports whether a master holds a light that keeps moon
// avoidance.
func (p *Pipeline) hasMoonFree(ctx context.Context, check *moonChecker, object, filter string) (bool, error) {
	var frames []app.Frame
	if err := p.db.WithContext(ctx).Table("frames f").Select("f.*").
		Joins("JOIN stack_frames sf ON sf.frame_id = f.id").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("sf.status = ? AND s.object = ? AND s.filter = ?", app.StackStatusAdded, object, filter).
		Scan(&frames).Error; err != nil {
		return false, err
	}
	for _, f := range frames {
		if !check.moonlit(ctx, f) {
			return true, nil
		}
	}
	return false, nil
}

// restackWithout stacks a master again from the subs it still holds, waiting for its
// target to be free.
func (p *Pipeline) restackWithout(ctx context.Context, id int) error {
	var stack app.Stack
	if err := p.db.WithContext(ctx).First(&stack, id).Error; err != nil {
		return err
	}
	for !p.hold(stack.Object) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	defer p.release(stack.Object)
	defer p.finished(stack.Object)
	var left int64
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).
		Where("stack_id = ? AND status = ? AND registered_key IS NOT NULL", stack.ID, app.StackStatusAdded).
		Count(&left).Error; err != nil {
		return err
	}
	if left == 0 {
		// Everything it had was moonlit; the old master stays until
		// something new comes.
		slog.Warn("Every sub of a master was moonlit; keeping the old one", "object", stack.Object, "filter", stack.Filter)
		return p.db.WithContext(ctx).Model(&stack).UpdateColumn("needs_rebuild", true).Error
	}
	acc, err := p.rebuild(ctx, &stack)
	if err != nil {
		return err
	}
	stack.RebuiltAtSubs = acc.Subs
	stack.NeedsRebuild = false
	return p.publish(ctx, &stack, acc)
}
