package period

import (
	"path/filepath"
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

// TestTESSSharedWindows runs the vet on 16040 with the other stars the
// TESS job fitted in its field. In sector 41 a flat 0.26 deep drop 2 h
// before one minimum shows in the star and all five stars within 8", and
// scattered light dims stars across the field at another minimum: those
// minima are set aside, and the rest are on time.
func TestTESSSharedWindows(t *testing.T) {
	t.Parallel()
	if underRace {
		t.Skip("too slow under the race detector")
	}
	series, comps := caseSeries(t, filepath.Join(tessCompsDir, "16040.csv.zst"))
	tc := readTESSComps(t, filepath.Join(tessCompsDir, "16040.comps.csv.zst"))
	opts := Options{Comparisons: func() [][]lc.Series { return comps }}
	r, err := Analyze(series, opts)
	if err != nil {
		t.Fatal(err)
	}
	before := rows(r)[check.TESS]
	t.Logf("without: %.6f %s %s", r.Ephemeris.Period, before.Status, before.Evidence)
	opts.TESSComparisons = func() []TESSComp { return tc }
	r, err = AnalyzeAt(series, r.Ephemeris, opts)
	if err != nil {
		t.Fatal(err)
	}
	row := rows(r)[check.TESS]
	t.Logf("with: %.6f %s %s", r.Ephemeris.Period, row.Status, row.Evidence)
	if before.Status != check.Fail {
		t.Errorf("without the other stars the TESS row should fail, got %s", before.Status)
	}
	if row.Status != check.Pass {
		t.Errorf("TESS row %s: %s", row.Status, row.Evidence)
	}
}
