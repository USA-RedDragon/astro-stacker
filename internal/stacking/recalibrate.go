package stacking

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

// Why a stacked light is calibrated again, best reason first.
const (
	// recalNoDark: it was stacked without a dark, and one matches now.
	recalNoDark = "no_dark"
	// recalGrown: its dark master was built from part of a set that has
	// since grown, as when lights were stacked while the darks were still
	// uploading.
	recalGrown = "dark_grew"
)

var recalOrder = map[string]int{recalNoDark: 0, recalGrown: 1}

// darkHistory is what is known of the dark a stacked light was calibrated
// with.
type darkHistory struct {
	noDark bool
	// used is the setup of its dark master, Count its frames; nil when the
	// light was stacked before masters were recorded.
	used *calmatch.Set
}

// recalReason says why a light calibrated with h should be calibrated
// again, given the dark that matches it now, or "" if it shouldn't.
func recalReason(h darkHistory, now calmatch.Match) string {
	switch {
	case now.Set == nil:
		return ""
	case h.noDark:
		return recalNoDark
	case h.used != nil && h.used.Master == "" && sameSetup(*h.used, *now.Set) && now.Set.Count > h.used.Count:
		return recalGrown
	}
	return ""
}

// sameSetup reports whether two dark sets were taken with the same
// exposure, gain, offset, setpoint and binning.
func sameSetup(a, b calmatch.Set) bool {
	for _, v := range [][2]float64{{a.Exposure, b.Exposure}, {a.Gain, b.Gain}, {a.Offset, b.Offset},
		{a.SetTemp, b.SetTemp}, {a.BinX, b.BinX}} {
		if math.IsNaN(v[0]) != math.IsNaN(v[1]) || (!math.IsNaN(v[0]) && v[0] != v[1]) {
			return false
		}
	}
	return true
}

// darkMasters returns the setup of every dark master a light may have
// recorded, by key: built masters that recorded theirs, and imported ones.
func (p *Pipeline) darkMasters(ctx context.Context) (map[string]calmatch.Set, error) {
	var cms []app.CalibrationMaster
	if err := p.db.WithContext(ctx).Where("type = ? AND exposure IS NOT NULL", "DARK").Find(&cms).Error; err != nil {
		return nil, err
	}
	out := make(map[string]calmatch.Set, len(cms)+len(coverage.Imported))
	for _, cm := range cms {
		out[cm.SetKey] = calmatch.Set{Type: cm.Type, Exposure: val(cm.Exposure), Gain: val(cm.Gain), Offset: val(cm.Offset),
			SetTemp: val(cm.SetTemp), BinX: val(cm.BinX), Count: cm.Frames}
	}
	for _, s := range coverage.Imported {
		if s.Type == "DARK" {
			out[importedKey(s)] = s
		}
	}
	return out, nil
}

// recalibrateDarks queues stacked lights for calibration again when a
// better dark matches them than the one they were calibrated with, and
// marks their masters to be rebuilt without the old versions. Only a
// settled dark set counts, and no more than RecalibrateLimit lights wait at
// once, so a new dark library reaches the masters a few hundred lights at a
// time.
func (p *Pipeline) recalibrateDarks(ctx context.Context) error {
	var rows []struct {
		ID         int
		Stack      int
		NoDark     bool
		DarkMaster *string
		Frame      app.Frame `gorm:"embedded;embeddedPrefix:f_"`
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.id, sf.stack_id AS stack, sf.no_dark, sf.dark_master, f.key AS f_key, f.night AS f_night, "+
			"f.filter AS f_filter, f.exposure AS f_exposure, f.gain AS f_gain, f.\"offset\" AS f_offset, "+
			"f.set_temp AS f_set_temp, f.bin_x AS f_bin_x, f.rotator AS f_rotator").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND sf.stack_id IS NOT NULL", app.StackStatusAdded).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return err
	}
	masters, err := p.darkMasters(ctx)
	if err != nil {
		return err
	}
	type due struct {
		id, stack int
		why       string
	}
	var found []due
	now := time.Now()
	for _, r := range rows {
		f := r.Frame
		if f.Night == nil || f.Exposure == nil || precalibrated(f) {
			continue
		}
		m := calmatch.Choose(calmatch.Group{
			Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
			SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator),
		}, sets).Dark
		if m.Set == nil || !p.settled(*m.Set, now) {
			continue
		}
		h := darkHistory{noDark: r.NoDark}
		if r.DarkMaster != nil {
			if s, ok := masters[*r.DarkMaster]; ok {
				h.used = &s
			}
		}
		if why := recalReason(h, m); why != "" {
			found = append(found, due{r.ID, r.Stack, why})
		}
	}
	if len(found) == 0 {
		return nil
	}

	var waiting int64
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("status = ?", app.StackStatusRecalibrate).
		Count(&waiting).Error; err != nil {
		return err
	}
	// Best reason first, then a master at a time, so a master is rebuilt
	// once for all its lights where the limit allows.
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if recalOrder[a.why] != recalOrder[b.why] {
			return recalOrder[a.why] < recalOrder[b.why]
		}
		if a.stack != b.stack {
			return a.stack < b.stack
		}
		return a.id < b.id
	})
	byReason := map[string]int{}
	for _, d := range found {
		byReason[d.why]++
	}
	take := found[:max(0, min(len(found), p.opts.RecalibrateLimit-int(waiting)))]
	if len(take) == 0 {
		slog.Info("Lights due a better dark wait for those being calibrated again", "due", len(found),
			"no_dark", byReason[recalNoDark], "dark_grew", byReason[recalGrown], "waiting", waiting)
		return nil
	}
	ids := make([]int, len(take))
	stacks := map[int]bool{}
	for i, d := range take {
		ids[i] = d.id
		stacks[d.stack] = true
	}
	stackIDs := make([]int, 0, len(stacks))
	for id := range stacks {
		stackIDs = append(stackIDs, id)
	}
	if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&app.StackFrame{}).Where("id IN ?", ids).
			UpdateColumns(map[string]any{"status": app.StackStatusRecalibrate, "next_attempt_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&app.Stack{}).Where("id IN ?", stackIDs).UpdateColumn("needs_rebuild", true).Error
	}); err != nil {
		return err
	}
	slog.Info("Calibrating stacked lights again with a better dark", "lights", len(take), "masters", len(stackIDs),
		"due", len(found), "no_dark", byReason[recalNoDark], "dark_grew", byReason[recalGrown],
		"left_for_later", len(found)-len(take))
	return nil
}
