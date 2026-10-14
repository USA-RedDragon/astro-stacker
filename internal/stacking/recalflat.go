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

// Why a stacked light is calibrated again for its flat, best reason first.
const (
	// recalFlatEpoch: its flat is from another dust epoch than the one
	// that matches now, as for lights calibrated across a dust change.
	recalFlatEpoch = "flat_epoch"
	// recalFlatUnknown: it was stacked before flats were recorded and no
	// flat it could have had is found.
	recalFlatUnknown = "flat_unknown"
	// recalFlatAngle: its flat was taken at another rotator angle, and
	// one within calmatch.RotationToleranceDeg matches now.
	recalFlatAngle = "flat_angle"
	// recalFlatChanged: its flat master was built from a set that has
	// since changed, as when lights were stacked while the flats were
	// still uploading.
	recalFlatChanged = "flat_changed"
)

func recalFlatOrder(why string) int {
	switch why {
	case recalFlatEpoch, recalFlatUnknown:
		return 0
	case recalFlatAngle:
		return 1
	default:
		return 2
	}
}

// flatHistory is what is known of the flat a stacked light was calibrated
// with: used when it was recorded and its set is still current, gone when
// it was recorded but its set has changed since, likely when it wasn't
// recorded (inferFlat).
type flatHistory struct {
	used   *calmatch.Set
	gone   bool
	likely *calmatch.Set
}

// flatRecalReason says why a light calibrated with h should be calibrated
// again, given the flat that matches it now, or "" if it shouldn't.
func flatRecalReason(g calmatch.Group, h flatHistory, now calmatch.Match) string {
	if now.Set == nil {
		return ""
	}
	if h.gone {
		return recalFlatChanged
	}
	was := h.used
	if was == nil {
		if was = h.likely; was == nil {
			return recalFlatUnknown
		}
	}
	if g.Epochs.Of(was.Night) != g.Epochs.Of(now.Set.Night) {
		return recalFlatEpoch
	}
	if !flatAngleOK(*was, g) && flatAngleOK(*now.Set, g) {
		return recalFlatAngle
	}
	return ""
}

func flatAngleOK(s calmatch.Set, g calmatch.Group) bool {
	if math.IsNaN(s.Rotator) || math.IsNaN(g.Rotator) {
		return true
	}
	d := math.Mod(math.Abs(s.Rotator-g.Rotator), 360)
	return math.Min(d, 360-d) <= calmatch.RotationToleranceDeg
}

// inferFlat is the flat a light stacked at "at", before flats were
// recorded, was most likely calibrated with. The stacker then took any flat
// set with a frame uploaded by then, preferring the lights' gain, then the
// nearest night, then a rotator angle within 1°, then the later night, and
// knew nothing of dust epochs.
func inferFlat(g calmatch.Group, sets []calmatch.Set, at time.Time) *calmatch.Set {
	var cands []calmatch.Set
	for _, s := range sets {
		if s.Type == frameTypeFlat && s.Filter == g.Filter && sameVal(s.BinX, g.BinX) &&
			!s.FirstUploaded.IsZero() && s.FirstUploaded.Before(at) {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sameGain := func(s calmatch.Set) bool { return sameVal(s.Gain, g.Gain) && sameVal(s.Offset, g.Offset) }
	night := func(s calmatch.Set) float64 { return math.Round(math.Abs(g.Night.Sub(s.Night).Hours() / 24)) }
	oldAngle := func(s calmatch.Set) bool {
		if math.IsNaN(s.Rotator) || math.IsNaN(g.Rotator) {
			return true
		}
		d := math.Mod(math.Abs(s.Rotator-g.Rotator), 360)
		return math.Min(d, 360-d) <= 1
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if gi, gj := sameGain(cands[i]), sameGain(cands[j]); gi != gj {
			return gi
		}
		if di, dj := night(cands[i]), night(cands[j]); di != dj {
			return di < dj
		}
		if ri, rj := oldAngle(cands[i]), oldAngle(cands[j]); ri != rj {
			return ri
		}
		return cands[i].Night.After(cands[j].Night)
	})
	return &cands[0]
}

func sameVal(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return a == b
}

// flatSetKeys maps the master key of every current flat set to the set.
func (p *Pipeline) flatSetKeys(ctx context.Context, sets []calmatch.Set) (map[string]calmatch.Set, error) {
	out := map[string]calmatch.Set{}
	for _, s := range sets {
		if s.Type != frameTypeFlat {
			continue
		}
		if s.Master != "" {
			out[importedKey(s)] = s
			continue
		}
		frames, err := coverage.SetFrames(ctx, p.db, s)
		if err != nil {
			return nil, err
		}
		out[setKey(s.Type, frames)] = s
	}
	return out, nil
}

// flatDue is a stacked light to calibrate again for its flat, and why.
type flatDue struct {
	id, stack int
	why       string
}

// dueForFlats finds the stacked lights a better flat matches than the one
// they were calibrated with (flatRecalReason). Only a settled flat set
// counts.
func (p *Pipeline) dueForFlats(ctx context.Context, now time.Time) ([]flatDue, error) {
	epochs, err := coverage.Epochs(ctx, p.db)
	if err != nil {
		return nil, err
	}
	return p.dueForFlatsWith(ctx, now, epochs)
}

func (p *Pipeline) dueForFlatsWith(ctx context.Context, now time.Time, epochs calmatch.Epochs) ([]flatDue, error) {
	var rows []struct {
		ID          int
		Stack       int
		FlatMaster  *string
		ProcessedAt time.Time
		Frame       app.Frame `gorm:"embedded;embeddedPrefix:f_"`
	}
	if err := p.db.WithContext(ctx).Table("stack_frames sf").
		Select("sf.id, sf.stack_id AS stack, sf.flat_master, sf.processed_at, f.key AS f_key, f.night AS f_night, "+
			"f.filter AS f_filter, f.exposure AS f_exposure, f.gain AS f_gain, f.\"offset\" AS f_offset, "+
			"f.set_temp AS f_set_temp, f.bin_x AS f_bin_x, f.rotator AS f_rotator").
		Joins("JOIN frames f ON f.id = sf.frame_id").
		Where("sf.status IN ? AND sf.stack_id IS NOT NULL", stackedStatuses()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	sets, err := coverage.Sets(ctx, p.db)
	if err != nil {
		return nil, err
	}
	current, err := p.flatSetKeys(ctx, sets)
	if err != nil {
		return nil, err
	}
	var found []flatDue
	for _, r := range rows {
		f := r.Frame
		if f.Night == nil || f.Exposure == nil || precalibrated(f) {
			continue
		}
		g := calmatch.Group{
			Night: *f.Night, Filter: f.Filter, Exposure: *f.Exposure, Gain: val(f.Gain), Offset: val(f.Offset),
			SetTemp: val(f.SetTemp), BinX: val(f.BinX), Rotator: val(f.Rotator), Epochs: epochs,
		}
		m := calmatch.Choose(g, sets).Flat
		if m.Set == nil || !p.settled(*m.Set, now) {
			continue
		}
		var h flatHistory
		if r.FlatMaster != nil {
			if s, ok := current[*r.FlatMaster]; ok {
				h.used = &s
			} else {
				h.gone = true
			}
		} else {
			h.likely = inferFlat(g, sets, r.ProcessedAt)
		}
		if why := flatRecalReason(g, h, m); why != "" {
			found = append(found, flatDue{r.ID, r.Stack, why})
		}
	}
	return found, nil
}

// recalibrateFlats queues stacked lights for calibration again when a
// better flat matches them than the one they were calibrated with
// (dueForFlats), as recalibrateDarks does for darks, and under the same
// RecalibrateLimit.
func (p *Pipeline) recalibrateFlats(ctx context.Context) error {
	now := time.Now()
	found, err := p.dueForFlats(ctx, now)
	if err != nil || len(found) == 0 {
		return err
	}
	var waiting int64
	if err := p.db.WithContext(ctx).Model(&app.StackFrame{}).Where("status = ?", app.StackStatusRecalibrate).
		Count(&waiting).Error; err != nil {
		return err
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if recalFlatOrder(a.why) != recalFlatOrder(b.why) {
			return recalFlatOrder(a.why) < recalFlatOrder(b.why)
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
		slog.Info("Lights due a better flat wait for those being calibrated again", "due", len(found),
			"flat_epoch", byReason[recalFlatEpoch], "flat_unknown", byReason[recalFlatUnknown],
			"flat_angle", byReason[recalFlatAngle], "flat_changed", byReason[recalFlatChanged], "waiting", waiting)
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
			UpdateColumns(map[string]any{columnStatus: app.StackStatusRecalibrate, columnNextAttemptAt: now}).Error; err != nil {
			return err
		}
		return tx.Model(&app.Stack{}).Where("id IN ?", stackIDs).UpdateColumn("needs_rebuild", true).Error
	}); err != nil {
		return err
	}
	slog.Info("Calibrating stacked lights again with a better flat", "lights", len(take), "masters", len(stackIDs),
		"due", len(found), "flat_epoch", byReason[recalFlatEpoch], "flat_unknown", byReason[recalFlatUnknown],
		"flat_angle", byReason[recalFlatAngle], "flat_changed", byReason[recalFlatChanged],
		"left_for_later", len(found)-len(take))
	return nil
}
