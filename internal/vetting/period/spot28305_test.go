package period

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

// vet28305 is source 28305 (Gaia DR3 171049722563343232, G 15.14), a
// spotted young M dwarf with a 3.833-day cycle: its ZTF points with
// catflags 0 from IRSA, timed by HJD, and the three TESS sectors (19, 59,
// 86) the TESS job made for the vet, as the vet loads them.
const vet28305 = "testdata/vet28305/28305.csv.zst"

// TestSpot28305 is the 2026-10-24 audit of 28305: its spot cycle drifts
// in phase from season to season (ZTF r 0.89, 0.03, 0.07, 0.04, 0.96,
// 0.94, 0.78, 0.88), and the vet said its odd and even cycles differ
// from all seasons folded together, though no more than one season's do.
// TESS's light curves have changes slower than a day taken out, and the
// vet said the cycle rises and falls by 0.00 mag in TESS against 0.02 to
// 0.06 mag in the TESS pixels.
func TestSpot28305(t *testing.T) {
	t.Parallel()
	series, _ := vetDumpSeries(t, vet28305)
	res, err := Analyze(series, Options{G: 15.14})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindSinusoidal || e.Period < 3.80 || e.Period > 3.87 {
		t.Fatalf("got %s at %.5f days, want sinusoidal at 3.833", e.Kind, e.Period)
	}
	rs := rows(res)
	if d := rs[check.Double]; d.Status != check.Pass {
		t.Errorf("double check %s: %s", d.Status, d.Evidence)
	}
	ts := rs[check.TESS]
	t.Logf("tess %s: %s", ts.Status, ts.Evidence)
	if strings.Contains(ts.Evidence, "Rises and falls by") || !strings.Contains(ts.Evidence, tessFlatWords) {
		t.Errorf("TESS row gives a flattened amplitude: %s", ts.Evidence)
	}
}
