package period

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const tessCompsDir = "testdata/tesscomps"

func readTESSComps(t *testing.T, path string) []TESSComp {
	t.Helper()
	type key struct {
		id  string
		sec int
	}
	by := map[key]*TESSComp{}
	var order []key
	for _, r := range readCSV(t, path) {
		sec, err := strconv.Atoi(r["sector"])
		if err != nil {
			t.Fatal(err)
		}
		k := key{r["id"], sec}
		c, ok := by[k]
		if !ok {
			c = &TESSComp{Sector: sec, Blended: r["blended"] == "1"}
			by[k] = c
			order = append(order, k)
		}
		c.T = append(c.T, num(t, r["bjd"])+2450000)
		c.F = append(c.F, num(t, r["flux"]))
		c.E = append(c.E, 0)
	}
	out := make([]TESSComp, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

// TestTESSSharedWindows runs the vet on confirmed binaries with the
// other stars the TESS job fitted in their fields. In 16040's sector 41 a
// flat 0.26 deep drop 2 h before one minimum shows in the star and all
// five stars within 8", and scattered light dims stars across the field
// at another minimum: those minima are set aside, and the rest are on
// time. 21142's minima are on time in all five sectors.
func TestTESSSharedWindows(t *testing.T) {
	t.Parallel()
	cases := []struct{ key, series, comps string }{
		{"16040", filepath.Join(tessCompsDir, "16040.csv.zst"), filepath.Join(tessCompsDir, "16040.comps.csv.zst")},
		{"21142", filepath.Join(confirmedDir, "21142.csv.zst"), filepath.Join(tessCompsDir, "21142.comps.csv.zst")},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			if underRace {
				t.Skip("too slow under the race detector")
			}
			series, comps := caseSeries(t, c.series)
			tc := readTESSComps(t, c.comps)
			opts := Options{Comparisons: func() [][]lc.Series { return comps }}
			r, err := Analyze(series, opts)
			if err != nil {
				t.Fatal(err)
			}
			before := rows(r)[check.TESS]
			t.Logf("without: %.6f %s %s", r.Ephemeris.Period, before.Status, before.Evidence)
			opts.TESSComparisons = func() []TESSComp { return tc }
			r, err = Analyze(series, opts)
			if err != nil {
				t.Fatal(err)
			}
			row := rows(r)[check.TESS]
			t.Logf("with: %.6f %s %s", r.Ephemeris.Period, row.Status, row.Evidence)
			if row.Status != check.Pass {
				t.Errorf("TESS row %s: %s", row.Status, row.Evidence)
			}
		})
	}
}

// TestTESSSharedWindowsLate moves 21142's sector 84 points, and the
// other stars' in that sector with them, by lateMin minutes, so the
// joint ephemeris cannot place its minima: with the other stars of the
// field at hand, the TESS row must still fail. Its TESS minima are timed
// to 1 to 3 minutes; a period fitted to all sectors leaves sector 84
// about half the offset off.
func TestTESSSharedWindowsLate(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("too slow under the race detector")
	}
	for _, lateMin := range []float64{-30, 30} {
		series, comps := caseSeries(t, filepath.Join(confirmedDir, "21142.csv.zst"))
		tc := readTESSComps(t, filepath.Join(tessCompsDir, "21142.comps.csv.zst"))
		for i := range series {
			if series[i].Survey != lc.TESS || series[i].Band != "S84" {
				continue
			}
			pts := slices.Clone(series[i].Points)
			for j := range pts {
				pts[j].BJD += lateMin / 1440
			}
			series[i].Points = pts
		}
		for i := range tc {
			if tc[i].Sector != 84 {
				continue
			}
			for j := range tc[i].T {
				tc[i].T[j] += lateMin / 1440
			}
		}
		opts := Options{Comparisons: func() [][]lc.Series { return comps }, TESSComparisons: func() []TESSComp { return tc }}
		r, err := Analyze(series, opts)
		if err != nil {
			t.Fatal(err)
		}
		row := rows(r)[check.TESS]
		t.Logf("sector 84 %+.0f min: P %.6f, %s %s", lateMin, r.Ephemeris.Period, row.Status, row.Evidence)
		if row.Status != check.Fail {
			t.Errorf("sector 84 %+.0f min: TESS row %s: %s", lateMin, row.Status, row.Evidence)
		}
	}
}
