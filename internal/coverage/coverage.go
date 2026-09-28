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
	// Round rotation to whole degrees so frames from one session group
	// together; matching uses a 1 degree tolerance anyway.
	const cols = `type, night, object, filter, exposure, gain, "offset", set_temp, bin_x, ROUND(rotator) as rotator, COUNT(*) as n`
	const group = `type, night, object, filter, exposure, gain, "offset", set_temp, bin_x, ROUND(rotator)`

	var cal []groupRow
	if err := db.WithContext(ctx).Table("frames").Select(cols).
		Where("type IN ? AND night IS NOT NULL AND index_error IS NULL", []string{"FLAT", "DARK", "BIAS"}).
		Group(group).Scan(&cal).Error; err != nil {
		return nil, fmt.Errorf("load calibration sets: %w", err)
	}
	sets := make([]calmatch.Set, 0, len(cal))
	for _, c := range cal {
		sets = append(sets, calmatch.Set{
			Type: c.Type, Night: c.Night, Filter: c.Filter, Exposure: val(c.Exposure),
			Gain: val(c.Gain), Offset: val(c.Offset), SetTemp: val(c.SetTemp),
			BinX: val(c.BinX), Rotator: val(c.Rotator), Count: c.N,
		})
	}

	q := db.WithContext(ctx).Table("frames").Select(cols).
		Where("type = ? AND night IS NOT NULL AND index_error IS NULL", "LIGHT")
	if object != "" {
		q = q.Where("object = ?", object)
	}
	var lights []groupRow
	if err := q.Group(group).Scan(&lights).Error; err != nil {
		return nil, fmt.Errorf("load lights: %w", err)
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
	return rows, nil
}

func toMatch(m calmatch.Match, dark bool) Match {
	out := Match{Quality: string(m.Quality), AgeDays: m.AgeDays, RotationMismatch: m.RotationMismatch}
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
