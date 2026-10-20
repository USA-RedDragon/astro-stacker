package period

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestSeasonHits: a 50-day cycle whose phase jumps between seasons is
// found season by season; white noise and a cycle at another period are
// not.
func TestSeasonHits(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	make1 := func(per, amp float64, jump bool) []pt {
		var p []pt
		for s := range 8 {
			phase := 0.0
			if jump {
				phase = float64(s) * 0.17 * 2 * math.Pi
			}
			for d := range 200 {
				if rng.Float64() < 0.4 {
					continue
				}
				tt := float64(s*365+d) + 0.1*rng.Float64()
				y := amp * math.Sin(2*math.Pi*tt/per+phase)
				p = append(p, pt{t: tt, y: y + 0.03*rng.NormFloat64(), e: 0.03})
			}
		}
		return p
	}
	if f, h := seasonHits(make1(50.2, 0.06, true), 50.2); f != 8 || h < 7 {
		t.Errorf("drifting cycle: %d of %d seasons, want nearly all of 8", h, f)
	}
	if f, h := seasonHits(make1(50.2, 0, false), 50.2); 3*h >= 2*f {
		t.Errorf("noise: %d of %d seasons find the cycle", h, f)
	}
	if f, h := seasonHits(make1(31, 0.06, false), 50.2); 3*h >= 2*f {
		t.Errorf("31-day cycle: %d of %d seasons find 50.2 days", h, f)
	}
	if f, _ := seasonHits(make1(80, 0.06, false), 80); f != 0 {
		t.Errorf("80 days in 200-day seasons: %d seasons fitted, want none", f)
	}
}
