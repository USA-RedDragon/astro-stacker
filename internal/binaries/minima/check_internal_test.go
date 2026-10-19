package minima

import (
	"math"
	"sync"
	"testing"
)

// TestRightTimingsRarelyDoubtful times nights on a 2 mag deep Algol dip
// like source 618's with flux noise of 0.1 of the normal flux, frames
// lost where the flux comes out at or below 0, alone and with a second
// filter. The two methods' agreement is judged against their chance
// spread at the fit, set for 1% of right timings to fall outside it;
// the old test against their combined errors called about 40% of these
// right timings doubtful. Frames lost in the dip are not in the made-up
// nights, so up to 4% is allowed.
func TestRightTimingsRarelyDoubtful(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	right, doubtful := 0, 0
	var wg sync.WaitGroup
	for chunk := range uint64(6) {
		wg.Go(func() {
			r := &rng{s: 41 + chunk*977}
			for i := range 15 {
				d := deepDip(2461000.5 + float64(i)*2.256397)
				truth := d.Mid + (r.uniform()-0.5)*0.4*d.Half
				var second func(dt float64) (float64, float64, bool)
				if chunk%2 == 1 {
					second = func(dt float64) (float64, float64, bool) {
						f := math.Pow(10, -0.4*1.5*d.Shape(dt)) + 0.1*r.normal()
						if f <= 0 {
							return 0, 0, false
						}
						return 0.2 - 2.5*math.Log10(f), 1.0857 * 0.1 / f, true
					}
				}
				m, _, ok := Time(deepNight(r, truth, d, 0.1, second), d)
				if !ok || math.Abs(m.T-truth) > 3*m.Err {
					continue
				}
				mu.Lock()
				right++
				if !m.Agree {
					doubtful++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if right < 80 {
		t.Fatalf("only %d of 90 nights timed right", right)
	}
	if float64(doubtful) > 0.04*float64(right) {
		t.Errorf("%d of %d right timings called doubtful", doubtful, right)
	}
}

// TestAgreesScalesWithChanceSpread checks the agreement test reads the
// difference about its chance mean, against the larger of the two
// spreads.
func TestAgreesScalesWithChanceSpread(t *testing.T) {
	t.Parallel()
	s := Spread{Mean: 0.002, SD: 0.001, BeadSD: 0.0005, N: 100}
	for _, c := range []struct {
		diff float64
		want bool
	}{
		{0.002, true},
		{0.002 + 2.5*0.001, true},
		{0.002 - 2.6*0.001, false},
		{0, true},
		{0.006, false},
	} {
		if got := agrees(c.diff, s); got != c.want {
			t.Errorf("difference %.4f: agrees %v, want %v", c.diff, got, c.want)
		}
	}
	s.BeadSD = 0.002
	if !agrees(0.002+4*0.001, s) {
		t.Error("the residuals' wider spread was not used")
	}
}
