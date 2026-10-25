package stacking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// How a dust rim streak (dustcheck.RimStreak) weighs on its light. A
// streak no deeper than RimFreeDepth, or that stands out less than
// dustcheck.RimMinZ, costs nothing: on 2026-10-17, the Moon 70° away,
// the Sculptor subs measured 0.04% to 0.14%.
// Deeper, the weight falls in proportion to the depth beyond it, to none
// at RimDropDepth, where the light is left out. 1% is where one moonlit
// night shows in a master: its subs at 1% bring the master to 0.3%, the
// depth the dust check counts a window shadow from (EpochMinDepth), once
// they hold 30% of its weight, which one night of a three-night target
// does. On 2026-10-22, under a 90% Moon, the subs measured 0.3% to 1.0%.
//
// The factor depends on the streak alone: the Moon's sky brightness
// already lowers a moonlit light's score, and a moonlit light without a
// streak keeps its full weight.
const (
	RimFreeDepth = 0.25
	RimDropDepth = 1.0
)

// rimFactor is the share of its weight a light with streak r keeps, and
// why, in plain words, when it keeps less than all of it.
func rimFactor(r dustcheck.Rim) (float64, string) {
	d := -r.Depth
	if d <= RimFreeDepth || r.Z < dustcheck.RimMinZ {
		return 1, ""
	}
	where := fmt.Sprintf("a dark streak %.2f%% of the sky deep along the rim of the dust shadow at x %d, y %d, from moonlight scattered onto the dust",
		d, r.X, r.Y)
	if d >= RimDropDepth {
		return 0, fmt.Sprintf("Left out: %s; from %.1f%% a streak shows in the master.", where, RimDropDepth)
	}
	f := math.Round((RimDropDepth-d)/(RimDropDepth-RimFreeDepth)*100) / 100
	return f, fmt.Sprintf("Weight lowered to %.0f%%: %s.", f*100, where)
}

// rimShadows is the dust catalog of the flat a light was calibrated with,
// or else of its filter's latest flat by the light's time.
func (p *Pipeline) rimShadows(ctx context.Context, f app.Frame, flat string) ([]dustcheck.Blob, error) {
	var df app.DustFlat
	if err := p.db.WithContext(ctx).Where("set_key = ?", flat).Limit(1).Find(&df).Error; err != nil {
		return nil, err
	}
	if df.ID != 0 && df.Catalog != "" {
		var cat []dustcheck.Blob
		if err := json.Unmarshal([]byte(df.Catalog), &cat); err != nil {
			return nil, fmt.Errorf("dust catalog of flat %d: %w", df.ID, err)
		}
		return cat, nil
	}
	at := *f.Night
	if f.DateObs != nil {
		at = *f.DateObs
	}
	cats, err := p.latestCatalogs(ctx, at)
	return cats[f.Filter], err
}

// rimWeight is a light's weight for its score and exposure with its rim
// streak's factor, whether the streak leaves it out, and why, in plain
// words, when the streak costs it weight.
func rimWeight(score, exposure float64, rim app.RimStreak, has bool) (float64, bool, string) {
	if !has || rim.Factor >= 1 {
		return score * exposure, false, ""
	}
	return score * exposure * rim.Factor, rim.Factor <= 0, rim.Note
}

// rimStreaks is the recorded rim streak of each of frames that has one.
func (p *Pipeline) rimStreaks(ctx context.Context, frames []int) (map[int]app.RimStreak, error) {
	out := map[int]app.RimStreak{}
	if len(frames) == 0 {
		return out, nil
	}
	var rows []app.RimStreak
	for i := 0; i < len(frames); i += 1000 {
		var part []app.RimStreak
		if err := p.db.WithContext(ctx).Where("frame_id IN ?", frames[i:min(i+1000, len(frames))]).Find(&part).Error; err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	for _, r := range rows {
		out[r.FrameID] = r
	}
	return out, nil
}

// RimVersion is how rim streaks are measured; lights measured by an older
// one are measured again. 1: rim sectors on the target's own structure in
// its master, resampled into the light's sensor frame, are passed over
// (dustcheck.RimStreak).
const RimVersion = 1

// rimCandidate is a kept dust check copy of a light in a master, not yet
// measured for its rim streak.
type rimCandidate struct {
	app.DustLightSub
	StackFrameID  int
	Status        string
	Score         float64
	Exposure      float64
	Weight        float64
	Error         *string
	RegisteredKey string
	StackID       int
	MasterKey     string
}

// rimTemplates holds the binned masters of one sweep by stack.
type rimTemplates map[int]dustcheck.Map

// RimNightSubs is how many of a target's lights of one night must show a
// streak on the same shadow before any of them is weighted for it, and
// RimSameShadow how close, in sensor pixels, two shadows' centres are to
// be the same (catalogs of different flats place a shadow a few pixels
// apart). Over the kept lights of 2026-10-13 to 2026-10-23 only three
// nights' targets reached it, the three nearest the Moon: 4 of 5
// Sagittarius star cloud lights on 2026-10-18 (Moon 26° away), and on
// 2026-10-22 5 of 11 Sculptor lights and 7 of 8 of M 15's (32°); noise
// and the targets' own structure, Andromeda's and the Garlic Nebula's
// among them, never streaked more than 2 of a night's on one shadow.
const (
	RimNightSubs  = 3
	RimSameShadow = 50
)

// measureRims measures the rim streak of each kept dust check copy of a
// light in a master (DustLightSub) not yet measured under RimVersion,
// against its master resampled into its sensor frame with the registered
// copy's map to the sensor, then weighs the lights of each night and
// target it measured (confirmRims).
func (p *Pipeline) measureRims(ctx context.Context) {
	if !p.opts.DustCheck {
		return
	}
	rows, err := p.rimCandidates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("Finding lights to measure for dust rim streaks failed", "error", err)
		}
		return
	}
	tmpls := rimTemplates{}
	type group struct {
		night  time.Time
		object string
	}
	groups := map[group]bool{}
	measured := 0
	for _, c := range rows {
		if p.stopping(ctx) {
			return
		}
		row, ok, err := p.measureRim(ctx, c, tmpls)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Could not measure a light's dust rim streak", "frame", c.FrameID, "error", err)
			}
			continue
		}
		if ok {
			measured++
			groups[group{row.Night, row.Object}] = true
		}
	}
	lowered, left := 0, 0
	marked := map[int]bool{}
	for g := range groups {
		lo, out, m, err := p.confirmRims(ctx, g.night, g.object)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("Could not weigh a night's lights by their dust rim streaks", "night", g.night.Format(time.DateOnly), "object", g.object, "error", err)
			}
			continue
		}
		lowered, left = lowered+lo, left+out
		for id := range m {
			marked[id] = true
		}
	}
	if measured > 0 {
		slog.Info("Measured lights for dust rim streaks", "lights", measured, "weighted_down", lowered, "left_out", left, "masters_to_restack", len(marked))
	}
}

// confirmRims weighs one target's lights of one night by their rim streaks:
// a streak counts once RimNightSubs of them show one on the same shadow.
// A light whose factor changed takes it (applyRim); it returns how many
// were weighted down and left out, and the masters marked for restacking.
func (p *Pipeline) confirmRims(ctx context.Context, night time.Time, object string) (lowered, left int, marked map[int]bool, err error) {
	marked = map[int]bool{}
	var rows []app.RimStreak
	if err := p.db.WithContext(ctx).Where("night = ? AND object = ?", night, object).Order("frame_id").Find(&rows).Error; err != nil {
		return 0, 0, nil, err
	}
	confirmed := rimConfirmed(rows)
	for _, r := range rows {
		factor, note := 1.0, ""
		if confirmed[r.FrameID] {
			factor, note = rimFactor(dustcheck.Rim{X: r.X, Y: r.Y, Depth: r.Depth, Z: r.Z})
		}
		if factor == r.Factor && note == r.Note {
			continue
		}
		r.Factor, r.Note = factor, note
		if err := p.db.WithContext(ctx).Model(&app.RimStreak{}).Where("id = ?", r.ID).
			UpdateColumns(map[string]any{"factor": factor, "note": note}).Error; err != nil {
			return lowered, left, marked, err
		}
		var sf app.StackFrame
		if err := p.db.WithContext(ctx).Where("frame_id = ? AND status IN ? AND stack_id IS NOT NULL", r.FrameID, stackedStatuses()).
			Limit(1).Find(&sf).Error; err != nil {
			return lowered, left, marked, err
		}
		if sf.ID == 0 {
			continue
		}
		c := rimCandidate{StackFrameID: sf.ID, Status: sf.Status, Score: sf.Score, Exposure: sf.Exposure,
			Weight: sf.Weight, Error: sf.Error, StackID: *sf.StackID}
		restack, out, err := p.applyRim(ctx, c, r)
		if err != nil {
			return lowered, left, marked, err
		}
		switch {
		case out:
			left++
		case factor < 1:
			lowered++
		}
		if restack {
			marked[c.StackID] = true
		}
	}
	return lowered, left, marked, nil
}

// rimConfirmed is the lights among one night's rows of a target whose
// streak would cost weight (rimFactor) and lies on a shadow that at least
// RimNightSubs of them show one on.
func rimConfirmed(rows []app.RimStreak) map[int]bool {
	var streaked []app.RimStreak
	for _, r := range rows {
		if f, _ := rimFactor(dustcheck.Rim{Depth: r.Depth, Z: r.Z}); f < 1 {
			streaked = append(streaked, r)
		}
	}
	out := map[int]bool{}
	for _, r := range streaked {
		n := 0
		for _, o := range streaked {
			if math.Hypot(float64(r.X-o.X), float64(r.Y-o.Y)) <= RimSameShadow {
				n++
			}
		}
		if n >= RimNightSubs {
			out[r.FrameID] = true
		}
	}
	return out
}

// rimCandidates are the kept lights in masters not yet measured under
// RimVersion, by master.
func (p *Pipeline) rimCandidates(ctx context.Context) ([]rimCandidate, error) {
	var rows []rimCandidate
	err := p.db.WithContext(ctx).Table("dust_light_subs").
		Select("dust_light_subs.*, sf.id AS stack_frame_id, sf.status, sf.score, sf.exposure, sf.weight, sf.error, sf.registered_key, sf.stack_id, s.master_key").
		Joins("JOIN stack_frames sf ON sf.frame_id = dust_light_subs.frame_id").
		Joins("JOIN stacks s ON s.id = sf.stack_id").
		Where("sf.status IN ? AND sf.registered_key IS NOT NULL AND s.master_key IS NOT NULL", stackedStatuses()).
		Where("dust_light_subs.frame_id NOT IN (SELECT frame_id FROM rim_streaks WHERE version >= ?)", RimVersion).
		Order("sf.stack_id, dust_light_subs.frame_id").Scan(&rows).Error
	return rows, err
}

// measureRim measures one kept light against its master and records it,
// keeping the factor it has until confirmRims weighs its night;
// ok is false when it can't be measured: no dust shadows, a master without
// sky, or a registered copy without a map to the sensor.
func (p *Pipeline) measureRim(ctx context.Context, c rimCandidate, tmpls rimTemplates) (app.RimStreak, bool, error) {
	master, have := tmpls[c.StackID]
	if !have {
		b, err := p.getBytes(ctx, c.MasterKey)
		if err != nil {
			return app.RimStreak{}, false, err
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			return app.RimStreak{}, false, fmt.Errorf("%s: %w", c.MasterKey, err)
		}
		master, _ = dustcheck.MasterMap(im.Plane(0), im.W, im.H)
		tmpls[c.StackID] = master
	}
	if master.W == 0 {
		return app.RimStreak{}, false, nil
	}
	reg, err := p.getBytes(ctx, c.RegisteredKey)
	if err != nil {
		return app.RimStreak{}, false, err
	}
	ri, err := imagedata.Decode(reg)
	if err != nil {
		return app.RimStreak{}, false, fmt.Errorf("%s: %w", c.RegisteredKey, err)
	}
	b, err := p.getBytes(ctx, c.Key)
	if err != nil {
		return app.RimStreak{}, false, err
	}
	sub, err := dustcheck.DecodeSub(b)
	if err != nil {
		return app.RimStreak{}, false, fmt.Errorf("%s: %w", c.Key, err)
	}
	tmpl, ok := dustcheck.SensorTemplate(master, ri.Numbers[imagedata.PropSensorMap], sub.W, sub.H)
	if !ok {
		return app.RimStreak{}, false, nil
	}
	var f app.Frame
	if err := p.db.WithContext(ctx).Where("id = ?", c.FrameID).Limit(1).Find(&f).Error; err != nil || f.ID == 0 || f.Night == nil {
		return app.RimStreak{}, false, err
	}
	shadows, err := p.rimShadows(ctx, f, c.FlatMaster)
	if err != nil {
		return app.RimStreak{}, false, err
	}
	r, ok := dustcheck.RimStreak(sub, tmpl, shadows)
	if !ok {
		return app.RimStreak{}, false, nil
	}
	row := app.RimStreak{FrameID: f.ID, Night: *f.Night, Object: f.Object, Filter: f.Filter,
		Depth: r.Depth, Z: r.Z, X: r.X, Y: r.Y, Angle: r.Angle, Radius: r.Radius,
		Factor: 1, Version: RimVersion, MeasuredAt: time.Now()}
	var prev app.RimStreak
	if err := p.db.WithContext(ctx).Where("frame_id = ?", f.ID).Limit(1).Find(&prev).Error; err != nil {
		return app.RimStreak{}, false, err
	}
	if prev.ID != 0 {
		row.ID, row.Factor, row.Note = prev.ID, prev.Factor, prev.Note
	}
	err = p.db.WithContext(ctx).Save(&row).Error
	return row, true, err
}

// applyRim gives a light in a master its weight with its rim streak's
// factor, or leaves it out below the cut; restack says its master needs
// restacking for it.
func (p *Pipeline) applyRim(ctx context.Context, c rimCandidate, row app.RimStreak) (restack, out bool, err error) {
	weight, out, why := rimWeight(c.Score, c.Exposure, row, true)
	cols := map[string]any{"weight": weight, columnError: optional(why)}
	if out {
		cols = map[string]any{columnStatus: app.StackStatusLowScore, columnProcessedAt: time.Now(),
			columnNextAttemptAt: time.Now().Add(p.opts.RetryAfter), columnError: why}
		restack = c.Status == app.StackStatusAdded
	} else {
		if weight == c.Weight && (why == "") == (c.Error == nil) {
			return false, false, nil
		}
		restack = c.Status == app.StackStatusAdded && (!(c.Weight > 0) || math.Abs(weight/c.Weight-1) > rescoreWeightTolerance)
	}
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&app.StackFrame{}).Where("id = ?", c.StackFrameID).UpdateColumns(cols).Error; err != nil {
			return err
		}
		if !restack {
			return nil
		}
		return tx.Model(&app.Stack{}).Where("id = ?", c.StackID).UpdateColumn("needs_rebuild", true).Error
	})
	return restack, out, err
}
