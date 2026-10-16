package coverage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"gorm.io/gorm"
)

// RunGap is the longest pause between two frames of one calibration run.
const RunGap = 30 * time.Minute

// Calibration run kinds.
const (
	RunDarks = "darks"
	RunFlats = "flats"
	RunBias  = "bias"
)

// RecentCalibration is the calibration runs taken in a window, newest first.
type RecentCalibration struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	NewestIndexed *time.Time       `json:"newest_indexed"`
	Runs          []CalibrationRun `json:"runs"`
}

// CalibrationRun is consecutive frames of one kind and setup with no pause
// longer than RunGap. EndedAt is the end of the last frame's exposure.
// Clean, Leak and Unchecked are the ingest check verdicts of darks and bias;
// flats have no check, so they carry none of the three.
type CalibrationRun struct {
	Kind       string    `json:"kind"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	TargetName string    `json:"target_name,omitempty"`
	Filter     string    `json:"filter,omitempty"`
	Exposure   *float64  `json:"exposure"`
	Gain       *float64  `json:"gain"`
	Offset     *float64  `json:"offset"`
	Binning    *float64  `json:"binning"`
	SetTemp    *float64  `json:"set_temp,omitempty"`
	Frames     int       `json:"frames"`
	Clean      *int      `json:"clean,omitempty"`
	Leak       *int      `json:"leak,omitempty"`
	Unchecked  *int      `json:"unchecked,omitempty"`
}

type recentFrame struct {
	Type      string
	Object    *string
	Filter    *string
	Exposure  *float64
	Gain      *float64
	Offset    *float64
	SetTemp   *float64
	BinX      *float64
	DateObs   time.Time
	LightLeak *float64
	CalCheck  *string
}

func runKind(typ string) string {
	switch typ {
	case darkType:
		return RunDarks
	case darkcheck.TypeBias:
		return RunBias
	}
	return RunFlats
}

func (f recentFrame) setup() string {
	if runKind(f.Type) == RunFlats {
		return fmt.Sprintf("%s|%s|%s|%v|%v|%v|%v", f.Type, sval(f.Object), sval(f.Filter), fptr(f.Exposure), fptr(f.Gain), fptr(f.Offset), fptr(f.BinX))
	}
	return fmt.Sprintf("%s|%v|%v|%v|%v|%v", f.Type, fptr(f.Exposure), fptr(f.Gain), fptr(f.Offset), fptr(f.SetTemp), fptr(f.BinX))
}

func sval(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// BuildRecentCalibration groups the darks, flats and bias taken in
// [from, to] into runs.
func BuildRecentCalibration(ctx context.Context, db *gorm.DB, from, to time.Time) (RecentCalibration, error) {
	types := []string{darkType, "FLAT", darkcheck.TypeBias}
	var frames []recentFrame
	if err := db.WithContext(ctx).Table("frames").
		Select(`type, object, filter, exposure, gain, "offset", set_temp, bin_x, date_obs, light_leak, cal_check`).
		Where("type IN ? AND index_error IS NULL AND date_obs >= ? AND date_obs <= ?", types, from, to).
		Scan(&frames).Error; err != nil {
		return RecentCalibration{}, fmt.Errorf("load recent calibration frames: %w", err)
	}
	var newest []struct{ IndexedAt *time.Time }
	if err := db.WithContext(ctx).Table("frames").Select("indexed_at").
		Where("type IN ? AND indexed_at IS NOT NULL", types).Order("indexed_at DESC").Limit(1).
		Scan(&newest).Error; err != nil {
		return RecentCalibration{}, fmt.Errorf("load the newest indexed calibration frame: %w", err)
	}
	out := RecentCalibration{From: from.UTC(), To: to.UTC(), Runs: calibrationRuns(frames)}
	if len(newest) == 1 && newest[0].IndexedAt != nil && !newest[0].IndexedAt.IsZero() {
		t := newest[0].IndexedAt.UTC()
		out.NewestIndexed = &t
	}
	return out, nil
}

func calibrationRuns(frames []recentFrame) []CalibrationRun {
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].DateObs.Before(frames[j].DateObs) })
	runs := []CalibrationRun{}
	var cur *CalibrationRun
	var setup string
	var last time.Time
	for _, f := range frames {
		at := f.DateObs.UTC()
		if cur == nil || f.setup() != setup || at.Sub(last) > RunGap {
			runs = append(runs, newRun(f, at))
			cur, setup = &runs[len(runs)-1], f.setup()
		}
		last = at
		cur.Frames++
		cur.EndedAt = at
		if f.Exposure != nil && *f.Exposure > 0 {
			cur.EndedAt = at.Add(time.Duration(*f.Exposure * float64(time.Second)))
		}
		if cur.Kind == RunFlats {
			continue
		}
		d := darkFrame{LightLeak: f.LightLeak, CalCheck: f.CalCheck}
		switch {
		case clean(d):
			*cur.Clean++
		case rejected(d):
			*cur.Leak++
		default:
			*cur.Unchecked++
		}
	}
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	return runs
}

func newRun(f recentFrame, at time.Time) CalibrationRun {
	r := CalibrationRun{Kind: runKind(f.Type), StartedAt: at, EndedAt: at,
		Exposure: f.Exposure, Gain: f.Gain, Offset: f.Offset, Binning: f.BinX}
	if r.Kind == RunFlats {
		r.TargetName, r.Filter = sval(f.Object), sval(f.Filter)
		return r
	}
	r.SetTemp = f.SetTemp
	r.Clean, r.Leak, r.Unchecked = new(int), new(int), new(int)
	return r
}
