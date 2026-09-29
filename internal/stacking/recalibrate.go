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
	// recalCloser: a dark set now matches whose setpoint is at least
	// CloserDarkC closer to the light's than its dark's was.
	recalCloser = "closer_dark"
)

var recalOrder = map[string]int{recalNoDark: 0, recalCloser: 1, recalGrown: 2}

// CloserDarkC is how much closer, in °C, a dark's setpoint must be to a
// stacked light's for the light to be calibrated again with it. Dark
// optimization scales a dark to the lights, which absorbs a degree or two;
// the hot pixel population drifts further than that.
const CloserDarkC = 2.0

// darkHistory is what is known of the dark a stacked light was calibrated
// with.
type darkHistory struct {
	noDark bool
	// used is the setup of its dark master, Count its frames; nil when the
	// light was stacked before masters were recorded.
	used *calmatch.Set
	// likely stands in for used when it wasn't recorded: the dark that
	// matched the light among the sets complete when it was stacked
	// (inferDark). nil when none was.
	likely *calmatch.Set
}

// recalReason says why a light calibrated with h should be calibrated
// again, given the dark that matches it now, or "" if it shouldn't.
func recalReason(g calmatch.Group, h darkHistory, now calmatch.Match) string {
	if now.Set == nil {
		return ""
	}
	if h.noDark {
		return recalNoDark
	}
	was := h.used
	if was == nil {
		// Only the setpoint is compared for lights with no record: which
		// part of a set their master held is anyone's guess.
		if was = h.likely; was == nil {
			return ""
		}
	}
	if calmatch.TempOff(g, *was)-calmatch.TempOff(g, *now.Set) >= CloserDarkC {
		return recalCloser
	}
	if h.used != nil && h.used.Master == "" && sameSetup(*h.used, *now.Set) && now.Set.Count > h.used.Count {
		return recalGrown
	}
	return ""
}

// inferDark is the dark a light stacked at "at", before masters were
// recorded, was most likely calibrated with: the best match among the dark
// sets completely uploaded by then. A set still arriving then gave a master
// of part of it, which is worth replacing too, so it doesn't count.
func inferDark(g calmatch.Group, sets []calmatch.Set, at time.Time) *calmatch.Set {
	var then []calmatch.Set
	for _, s := range sets {
		if s.Type == "DARK" && (s.Master != "" || (!s.Uploaded.IsZero() && s.Uploaded.Before(at))) {
			then = append(then, s)
		}
	}
	return calmatch.Choose(g, then).Dark.Set
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

// darkDue is a stacked light to calibrate again, and why.
type darkDue struct {
	id, stack int
	why       string
}

// dueForDarks finds the stacked lights a better dark matches than the one
// they were calibrated with (recalReason). Only a settled dark set counts.
func (p *Pipeline) dueForDarks(ctx context.Context, now time.Time) ([]darkDue, error) {
	var rows []struct {
		ID          int
		Stack       int
		NoDark      bool
		DarkMaster  *string
		ProcessedAt time.Time
		Frame       app.Frame `gorm:"embedded;embeddedPrefix:f_"`
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.id, sf.stack_id AS stack, COALESCE(sf.no_dark, false) AS no_dark, sf.dark_master, sf.processed_at, f.key AS f_key, f.night AS f_night, "+
			"f.filter AS f_filter, f.exposure AS f_exposure, f.gain AS f_gain, f.\"offset\" AS f_offset, "+
			"f.set_temp AS f_set_temp, f.bin_x AS f_bin_x, f.rotator AS f_rotator").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status = ? AND sf.stack_id IS NOT NULL", app.StackStatusAdded).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return nil, err
	}
	masters, err := p.darkMasters(ctx)
	if err != nil {
		return nil, err
	}
	var found []darkDue
	for _, r := range rows {
		f := r.Frame
		if f.Night == nil || f.Exposure == nil || precalibrated(f) {
			continue
		}
		g := calmatch.Group{
			Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
			SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator),
		}
		m := calmatch.Choose(g, sets).Dark
		if m.Set == nil || !p.settled(*m.Set, now) {
			continue
		}
		h := darkHistory{noDark: r.NoDark}
		switch {
		case r.DarkMaster != nil:
			if s, ok := masters[*r.DarkMaster]; ok {
				h.used = &s
			}
		case !r.NoDark:
			h.likely = inferDark(g, sets, r.ProcessedAt)
		}
		if why := recalReason(g, h, m); why != "" {
			found = append(found, darkDue{r.ID, r.Stack, why})
		}
	}
	return found, nil
}

// recalibrateDarks queues stacked lights for calibration again when a
// better dark matches them than the one they were calibrated with
// (dueForDarks), and marks their masters to be rebuilt without the old
// versions. No more than RecalibrateLimit lights wait at once, so a new dark
// library reaches the masters a few hundred lights at a time.
func (p *Pipeline) recalibrateDarks(ctx context.Context) error {
	now := time.Now()
	found, err := p.dueForDarks(ctx, now)
	if err != nil || len(found) == 0 {
		return err
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
			"no_dark", byReason[recalNoDark], "closer_dark", byReason[recalCloser], "dark_grew", byReason[recalGrown],
			"waiting", waiting)
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
		"due", len(found), "no_dark", byReason[recalNoDark], "closer_dark", byReason[recalCloser],
		"dark_grew", byReason[recalGrown], "left_for_later", len(found)-len(take))
	return nil
}
