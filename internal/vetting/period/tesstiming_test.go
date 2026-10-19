package period

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// TestTESSTimingBlendedSectors runs the vet on confirmed binaries whose
// TESS sectors blend the star differently. 45029's sector 82 shows its
// dips at about a tenth of the depth sectors 41 and 55 show, and 21142's
// sector 57 at about a quarter; sector 78 and 85 caught three of its deep
// dips on one side only. Every dip TESS times must be on time.
func TestTESSTimingBlendedSectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key, path string
		faint     string
	}{
		{"45029", filepath.Join(confirmedDir, "45029.csv.zst"), "sector 82"},
		{"21142", filepath.Join(confirmedDir, "21142.csv.zst"), ""},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			if underRace {
				t.Skip("too slow under the race detector")
			}
			series, comps := caseSeries(t, c.path)
			r, err := Analyze(series, Options{Comparisons: func() [][]lc.Series { return comps }})
			if err != nil {
				t.Fatal(err)
			}
			row := rows(r)[check.TESS]
			t.Logf("%.6f %s %s", r.Ephemeris.Period, row.Status, row.Evidence)
			if row.Status != check.Pass {
				t.Errorf("TESS row %s: %s", row.Status, row.Evidence)
			}
			if c.faint != "" && !strings.Contains(row.Evidence, c.faint) {
				t.Errorf("TESS row does not name %s as blended: %s", c.faint, row.Evidence)
			}
		})
	}
}
