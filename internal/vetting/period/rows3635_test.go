package period

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// ephem3635 is source 3635's orbit from the audit: two nearly equal
// eclipses every 7.839490 days, Min I at BJD 2458321.28364.
func ephem3635() *Ephemeris {
	return &Ephemeris{Period: 7.839490, T0: 2458321.28364, Kind: KindEclipsing, Eclipses: []Eclipse{
		{Phase: 0, Width: 0.0158, Shape: 1.39, Depth: map[string]float64{}},
		{Phase: 0.5, Width: 0.0158, Shape: 1.39, Depth: map[string]float64{}},
	}}
}

// half3635 is the one-dip ephemeris the star:15 vet reported for 3635,
// at half the true period.
func half3635() *Ephemeris {
	return &Ephemeris{Period: 3.919735367653492, T0: 2458317.3639296764, Kind: KindEclipsing, Eclipses: []Eclipse{
		{Phase: 0, Width: 0.03166251527803854, Shape: 1.39416297165816, Depth: map[string]float64{}},
	}}
}

// TestRows3635 is source 3635's survey rows at its true period, and at
// the half period the star:15 vet reported. NEOWISE
// W1 shows the dips at about nine sigma and W2 at under three sigma but
// as deep as the other bands allow, so the NEOWISE row passes.
func TestRows3635(t *testing.T) {
	t.Parallel()
	series, comps := vetDumpSeries(t, vet3635)
	for _, c := range []struct {
		name string
		e    *Ephemeris
	}{{"true", ephem3635()}, {"half", half3635()}} {
		t.Run(c.name, func(t *testing.T) {
			res, err := AnalyzeAt(series, c.e, opts3635(comps))
			if err != nil {
				t.Fatal(err)
			}
			checkRows3635(t, res)
		})
	}
}

func checkRows3635(t *testing.T, res *Result) {
	t.Helper()
	for _, f := range res.Fits {
		t.Logf("%s %s in %d+%d depths %.3f %.3f agrees %v shows %v", f.Survey, f.Band, f.InDip1, f.InDip2, f.Depth1, f.Depth2, f.Agrees, f.Shows)
	}
	r := rows(res)
	for _, id := range []string{check.ZTF, check.ATLAS, check.TESS, check.NEOWISE} {
		t.Logf("%s %s: %s", id, r[id].Status, r[id].Evidence)
	}
	if n := r[check.NEOWISE]; n.Status != check.Pass || strings.Contains(n.Evidence, "not fainter") {
		t.Errorf("NEOWISE %s: %s; want passed", n.Status, n.Evidence)
	}
	for _, f := range res.Fits {
		if f.Survey == lc.NEOWISE && !f.Agrees {
			t.Errorf("NEOWISE %s contradicts the dips at depths %.3f and %.3f", f.Band, f.Depth1, f.Depth2)
		}
	}
}

// TestContradicts is the band test on 3635's NEOWISE W2, under three
// sigma but as deep as the other bands allow, and on a band whose dips
// are flat where the others show 0.2 mag.
func TestContradicts(t *testing.T) {
	t.Parallel()
	if contradicts(0.159, 0.055, 0.13) {
		t.Error("W2 at 0.159 +- 0.055 contradicts a 0.13 mag dip")
	}
	if !contradicts(0.01, 0.02, 0.2) {
		t.Error("a flat band at 0.01 +- 0.02 does not contradict a 0.2 mag dip")
	}
}
