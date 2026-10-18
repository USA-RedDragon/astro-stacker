package discoveries

import (
	"context"
	"testing"
)

func TestASourceHeldForLocalNoiseIsStillLookedUp(t *testing.T) {
	t.Parallel()
	// AT 2025xtl stood at 5.16 against the noise of M 31's bulge in the app's
	// own aperture: a real nova held for the noise around it must still be
	// matched to its TNS entry.
	c := newSource(100, 100)
	c.LocalSNR, c.LowConf = 4.3, true
	asked := 0
	lookup := func(context.Context, float64, float64, float64) ([]Alert, []string, []string) {
		asked++
		return []Alert{{Source: sourceTNS, ID: "AT 2025xtl", SepArc: 0.4}}, []string{sourceTNS}, nil
	}
	ls, tracks, _, failed := Classify(context.Background(), &Result{Candidates: []Candidate{c}}, Known{Galaxies: []Galaxy{m31Galaxy()}}, lookup)
	if asked != 1 || len(tracks) != 0 || len(failed) != 0 || ls[0].Class != ClassKnownTransient {
		t.Errorf("asked %d times, class %s", asked, ls[0].Class)
	}
}
