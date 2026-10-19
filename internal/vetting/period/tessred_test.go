package period

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// tessRedDir holds the light curves the vet read for 1688, 2301 and 6014
// on 2026-10-19: ZTF, ATLAS, TESS and NEOWISE, the ZTF comparison stars,
// and the other stars of the TESS job.
const tessRedDir = "testdata/tessred"

func tessRedCase(t *testing.T, key string) ([]lc.Series, Options) {
	t.Helper()
	series, comps := caseSeries(t, filepath.Join(tessRedDir, key+".csv.zst"))
	tc := readTESSComps(t, filepath.Join(tessRedDir, key+".comps.csv.zst"))
	return series, Options{
		Comparisons:     func() [][]lc.Series { return comps },
		TESSComparisons: func() []TESSComp { return tc },
	}
}

// TestTESSTimingRedNoise runs the vet on confirmed binaries whose TESS
// sectors drift on their dips' time scale. 1688's sector 41 wanders by
// 0.05 mag over hours against its 0.14 mag dip, which pulls one minimum
// 61 min late and fills in another. 2301's sectors show its 0.3 to 0.4
// mag dips at 0.005 mag, and sector 84 drifts as much: a fit there found
// a "minimum" a day early. In 6014's sector 41 one primary is 1.9 times
// as deep as the others for an hour, 11.6 min from its time. Every dip
// TESS times must be on time.
func TestTESSTimingRedNoise(t *testing.T) {
	t.Parallel()
	cases := []struct{ key, blended string }{
		{"1688", ""},
		{"2301", "sectors 17 and 84"},
		{"6014", ""},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			if underRace {
				t.Skip("too slow under the race detector")
			}
			series, opts := tessRedCase(t, c.key)
			r, err := Analyze(series, opts)
			if err != nil {
				t.Fatal(err)
			}
			row := rows(r)[check.TESS]
			t.Logf("%.6f %s %s", r.Ephemeris.Period, row.Status, row.Evidence)
			if row.Status != check.Pass {
				t.Errorf("TESS row %s: %s", row.Status, row.Evidence)
			}
			if c.blended != "" && !strings.Contains(row.Evidence, c.blended) {
				t.Errorf("TESS row does not name %s as blended: %s", c.blended, row.Evidence)
			}
		})
	}
}

// TestTESSTimingRedNoiseLate moves one clean sector of 1688 and 6014,
// and the other stars' points in it, by lateMin minutes: the slow drifts
// of the other sectors must not hide the offset. A period fitted to all
// sectors takes up about half of it; 1688's sector 55 minima are timed to
// 8 to 13 min and 6014's sector 82 minima to 3 to 4 min.
func TestTESSTimingRedNoiseLate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key, band string
		sector    int
		lateMin   float64
	}{
		{"1688", "S55", 55, 90},
		{"1688", "S55", 55, -90},
		{"6014", "S82", 82, 30},
		{"6014", "S82", 82, -30},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			if underRace {
				t.Skip("too slow under the race detector")
			}
			series, opts := tessRedCase(t, c.key)
			for i := range series {
				if series[i].Survey != lc.TESS || series[i].Band != c.band {
					continue
				}
				pts := slices.Clone(series[i].Points)
				for j := range pts {
					pts[j].BJD += c.lateMin / 1440
				}
				series[i].Points = pts
			}
			tc := opts.TESSComparisons()
			for i := range tc {
				if tc[i].Sector != c.sector {
					continue
				}
				tc[i].T = slices.Clone(tc[i].T)
				for j := range tc[i].T {
					tc[i].T[j] += c.lateMin / 1440
				}
			}
			r, err := Analyze(series, opts)
			if err != nil {
				t.Fatal(err)
			}
			row := rows(r)[check.TESS]
			t.Logf("sector %d %+.0f min: P %.6f, %s %s", c.sector, c.lateMin, r.Ephemeris.Period, row.Status, row.Evidence)
			if row.Status != check.Fail {
				t.Errorf("sector %d %+.0f min: TESS row %s: %s", c.sector, c.lateMin, row.Status, row.Evidence)
			}
		})
	}
}
