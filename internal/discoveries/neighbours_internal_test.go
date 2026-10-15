package discoveries

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestNeighboursAboutAsBrightJudgeAStar replays the North America Nebula
// Panel 2 night of 2025-12-17: among its G 13 to 18 neighbours the faint
// ones' changes were mostly their own noise, a spread of 0.062 that hid the
// 0.15 eclipse of a G 14.2 star, while the stars about as bright spread by
// under 0.02.
func TestNeighboursAboutAsBrightJudgeAStar(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 5))
	pts, vals, refs := make([][2]float64, 0, 400), make([]float64, 0, 400), make([]float64, 0, 400)
	for i := range 400 {
		x, y := 300+rng.Float64()*900, 300+rng.Float64()*900
		ref, noise := 2.0, 0.015
		if i%5 != 0 {
			ref, noise = 0.1, 0.09
		}
		pts = append(pts, [2]float64{x, y})
		vals = append(vals, noise*rng.NormFloat64())
		refs = append(refs, ref)
	}
	l := localFrac{grid: NewGrid(pts, 64), vals: vals, refs: refs}
	const star, change = 1.5, -0.15
	m, spread, ok := l.around(750, 750, star)
	if !ok || math.Abs(change-m) < localSigma*spread {
		t.Fatalf("about as bright: median %.3f spread %.3f, the eclipse is %.1f spreads out", m, spread, math.Abs(change-m)/spread)
	}
	m, spread, _ = l.around(750, 750, 0)
	if math.Abs(change-m) >= localSigma*spread {
		t.Errorf("any brightness: median %.3f spread %.3f should have hidden the eclipse", m, spread)
	}
	if _, _, ok := (localFrac{grid: NewGrid(pts[:30], 64), vals: vals[:30], refs: refs[:30]}).around(750, 750, star); !ok {
		t.Error("too few stars about as bright did not fall back to any brightness")
	}
}
