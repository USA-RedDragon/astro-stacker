// Package coverage reports, for every night of lights, which flat, dark and
// bias sets calibrate them and how well they match.
package coverage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/pixinsight-worker/internal/calmatch"
	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
	"gorm.io/gorm"
)

// Row is one group of lights and its calibration.
type Row struct {
	Night    string   `json:"night"`
	Object   string   `json:"object"`
	Filter   string   `json:"filter"`
	Exposure float64  `json:"exposure"`
	Gain     *float64 `json:"gain"`
	Offset   *float64 `json:"offset"`
	SetTemp  *float64 `json:"set_temp"`
	Rotator  *float64 `json:"rotator"`
	Lights   int      `json:"lights"`
	Flat     Match    `json:"flat"`
	Dark     Match    `json:"dark"`
	Bias     Match    `json:"bias"`
}

type Match struct {
	Quality string   `json:"quality"`
	Night   string   `json:"night,omitempty"`
	Frames  int      `json:"frames,omitempty"`
	AgeDays int      `json:"age_days"`
	TempOff *float64 `json:"temp_off,omitempty"`
	SetTemp *float64 `json:"set_temp,omitempty"`
	// RotationMismatch marks a flat taken at another rotator angle.
	RotationMismatch bool `json:"rotation_mismatch,omitempty"`
	// Scaled marks a dark whose thermal signal must be scaled to the lights.
	Scaled bool `json:"scaled,omitempty"`
}

type groupRow struct {
	Type     string
	Night    time.Time
	Object   string
	Filter   string
	Exposure *float64
	Gain     *float64
	Offset   *float64
	SetTemp  *float64
	BinX     *float64
	Rotator  *float64
	N        int
}

func val(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func ptr(v float64) *float64 {
	if math.IsNaN(v) {
		return nil
	}
	return &v
}

// Report computes coverage for all lights, or for one object when object is set.
func Report(ctx context.Context, db *gorm.DB, object string) ([]Row, error) {
	rows, _, err := report(ctx, db, object)
	return rows, err
}

// Gaps computes the dark library capture list across all lights.
func Gaps(ctx context.Context, db *gorm.DB) ([]DarkGap, error) {
	rows, sets, err := report(ctx, db, "")
	if err != nil {
		return nil, err
	}
	return DarkGaps(rows, sets), nil
}

// Frames of a session are grouped on these columns. Rotation is rounded to
// whole degrees so frames from one session group together; matching uses a
// 1 degree tolerance anyway.
const (
	groupCols = `type, night, object, filter, exposure, gain, "offset", set_temp, bin_x, ROUND(rotator) as rotator, COUNT(*) as n`
	groupBy   = `type, night, object, filter, exposure, gain, "offset", set_temp, bin_x, ROUND(rotator)`
)

// Sets loads every flat, dark and bias set.
func Sets(ctx context.Context, db *gorm.DB) ([]calmatch.Set, error) {
	var cal []groupRow
	if err := db.WithContext(ctx).Table("frames").Select(groupCols).
		Where("type IN ? AND night IS NOT NULL AND index_error IS NULL", []string{"FLAT", "DARK", "BIAS"}).
		Group(groupBy).Scan(&cal).Error; err != nil {
		return nil, fmt.Errorf("load calibration sets: %w", err)
	}
	sets := make([]calmatch.Set, 0, len(cal))
	for _, c := range cal {
		sets = append(sets, calmatch.Set{
			Type: c.Type, Night: c.Night, Object: c.Object, Filter: c.Filter, Exposure: val(c.Exposure),
			Gain: val(c.Gain), Offset: val(c.Offset), SetTemp: val(c.SetTemp),
			BinX: val(c.BinX), Rotator: val(c.Rotator), Count: c.N,
		})
	}
	return sets, nil
}

// SetFrames returns the frames that make up a set, matching the grouping in
// Sets exactly, with missing values matched as NULL.
func SetFrames(ctx context.Context, db *gorm.DB, s calmatch.Set) ([]app.Frame, error) {
	q := db.WithContext(ctx).Where("type = ? AND night = ? AND object = ? AND filter = ? AND index_error IS NULL",
		s.Type, s.Night, s.Object, s.Filter)
	for col, v := range map[string]float64{
		"exposure": s.Exposure, "gain": s.Gain, `"offset"`: s.Offset, "set_temp": s.SetTemp, "bin_x": s.BinX,
	} {
		if math.IsNaN(v) {
			q = q.Where(col + " IS NULL")
		} else {
			q = q.Where(col+" = ?", v)
		}
	}
	if math.IsNaN(s.Rotator) {
		q = q.Where("rotator IS NULL")
	} else {
		q = q.Where("ROUND(rotator) = ?", s.Rotator)
	}
	var frames []app.Frame
	if err := q.Order("key").Find(&frames).Error; err != nil {
		return nil, fmt.Errorf("load frames of %s set: %w", s.Type, err)
	}
	return frames, nil
}

func report(ctx context.Context, db *gorm.DB, object string) ([]Row, []calmatch.Set, error) {
	sets, err := Sets(ctx, db)
	if err != nil {
		return nil, nil, err
	}

	q := db.WithContext(ctx).Table("frames").Select(groupCols).
		Where("type = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT")
	if object != "" {
		q = q.Where("object = ?", object)
	}
	var lights []groupRow
	if err := q.Group(groupBy).Scan(&lights).Error; err != nil {
		return nil, nil, fmt.Errorf("load lights: %w", err)
	}

	rows := make([]Row, 0, len(lights))
	for _, l := range lights {
		res := calmatch.Choose(calmatch.Group{
			Night: l.Night, Filter: l.Filter, Exposure: val(l.Exposure), Gain: val(l.Gain),
			Offset: val(l.Offset), SetTemp: val(l.SetTemp), BinX: val(l.BinX), Rotator: val(l.Rotator),
		}, sets)
		rows = append(rows, Row{
			Night: l.Night.Format("2006-01-02"), Object: l.Object, Filter: l.Filter,
			Exposure: val(l.Exposure), Gain: l.Gain, Offset: l.Offset, SetTemp: l.SetTemp,
			Rotator: l.Rotator, Lights: l.N,
			Flat: toMatch(res.Flat, false), Dark: toMatch(res.Dark, true), Bias: toMatch(res.Bias, false),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Night != rows[j].Night {
			return rows[i].Night > rows[j].Night
		}
		if rows[i].Object != rows[j].Object {
			return rows[i].Object < rows[j].Object
		}
		return rows[i].Filter < rows[j].Filter
	})
	return rows, sets, nil
}

func toMatch(m calmatch.Match, dark bool) Match {
	out := Match{Quality: string(m.Quality), AgeDays: m.AgeDays, RotationMismatch: m.RotationMismatch, Scaled: m.Scaled}
	if m.Set != nil {
		out.Night = m.Set.Night.Format("2006-01-02")
		out.Frames = m.Set.Count
		if dark {
			out.TempOff = ptr(m.TempOff)
			out.SetTemp = ptr(m.Set.SetTemp)
		}
	}
	return out
}

// DarkLadder is the set of setpoints the dark library is kept at. Lights
// between rungs use the nearest rung's darks, scaled.
var DarkLadder = []float64{-25, -15, -5, 5}

// DarkGap is a dark set the library should have but doesn't.
type DarkGap struct {
	Gain    *float64 `json:"gain"`
	Offset  *float64 `json:"offset"`
	SetTemp float64  `json:"set_temp"`
	Lights  int      `json:"lights"`
	Nights  int      `json:"nights"`
	Latest  string   `json:"latest_night"`
}

// DarkGaps lists the ladder rungs, per gain and offset, that lights need but
// no dark set within SetTempExactC covers. It is the capture list for the
// dark library.
func DarkGaps(rows []Row, have []calmatch.Set) []DarkGap {
	type key struct {
		gain, offset, rung float64
	}
	covered := func(k key) bool {
		for _, s := range have {
			if s.Type == "DARK" && same(s.Gain, k.gain) && same(s.Offset, k.offset) &&
				math.Abs(s.SetTemp-k.rung) <= calmatch.SetTempExactC {
				return true
			}
		}
		return false
	}
	agg := map[key]*DarkGap{}
	nights := map[key]map[string]bool{}
	for _, r := range rows {
		// Without a recorded setpoint, gain or offset there is no dark set to
		// ask for. NaN keys would also never match themselves in the map.
		if r.SetTemp == nil || r.Gain == nil || r.Offset == nil {
			continue
		}
		k := key{val(r.Gain), val(r.Offset), nearestRung(*r.SetTemp)}
		if covered(k) {
			continue
		}
		g, ok := agg[k]
		if !ok {
			g = &DarkGap{Gain: r.Gain, Offset: r.Offset, SetTemp: k.rung}
			agg[k] = g
			nights[k] = map[string]bool{}
		}
		g.Lights += r.Lights
		nights[k][r.Night] = true
		if r.Night > g.Latest {
			g.Latest = r.Night
		}
	}
	out := make([]DarkGap, 0, len(agg))
	for k, g := range agg {
		g.Nights = len(nights[k])
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lights > out[j].Lights })
	return out
}

func nearestRung(t float64) float64 {
	best := DarkLadder[0]
	for _, r := range DarkLadder[1:] {
		if math.Abs(r-t) < math.Abs(best-t) {
			best = r
		}
	}
	return best
}

func same(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return a == b
}
