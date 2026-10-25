package period

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// seasonSine is eight ZTF-like seasons of g and r points on a cycle of the given
// period: each season's cycle shifted by shift(season) cycles, with a
// cycle at twice the period of share times the amplitude added.
func seasonSine(period, share float64, shift func(int) float64) []lc.Series {
	const amp = 0.05
	rng := lcg(2828)
	g := lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
	r := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	for season := range 8 {
		for i := range 90 {
			tt := 2458400 + 365.25*float64(season) + float64(i)*2.1 + 0.3*rng.next()
			x := (tt-2459000.25)/period + shift(season)
			m := amp*math.Cos(2*math.Pi*x) + share*amp*math.Cos(math.Pi*(tt-2459000.25)/period)
			g.Points = append(g.Points, lc.Point{BJD: tt, Mag: 15 + 1.2*m + 0.01*rng.gauss(), Err: 0.01})
			r.Points = append(r.Points, lc.Point{BJD: tt + 0.03, Mag: 14.5 + m + 0.01*rng.gauss(), Err: 0.01})
		}
	}
	return []lc.Series{g, r}
}

// TestDoubleDriftingSpots is a spotted star like 28305 whose cycle moves
// in phase from season to season: its odd and even cycles match within
// each season, so the double check must pass.
func TestDoubleDriftingSpots(t *testing.T) {
	t.Parallel()
	const P = 3.833
	phases := []float64{0.89, 0.03, 0.07, 0.04, 0.96, 0.94, 0.78, 0.88}
	res, err := Analyze(seasonSine(P, 0, func(s int) float64 { return phases[s] }), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindSinusoidal || math.Abs(e.Period-P) > 0.01 {
		t.Fatalf("got %s at %.5f, want sinusoidal at %.3f", e.Kind, e.Period, P)
	}
	if d := rows(res)[check.Double]; d.Status != check.Pass {
		t.Errorf("double check %s: %s", d.Status, d.Evidence)
	}
}

// TestDoubleUnequalCycles is a star whose odd and even cycles differ in
// every season by the same cycle at twice the period: the vet must find
// twice the period or fail the double check.
func TestDoubleUnequalCycles(t *testing.T) {
	t.Parallel()
	const P = 1.9171
	res, err := Analyze(seasonSine(P, 0.5, func(int) float64 { return 0 }), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	d := rows(res)[check.Double]
	t.Logf("%s at %.5f; double %s: %s", e.Kind, e.Period, d.Status, d.Evidence)
	if math.Abs(e.Period-2*P) > 0.01 && d.Status != check.Fail {
		t.Errorf("found %.5f days and the double check %s: %s", e.Period, d.Status, d.Evidence)
	}
}

// TestParityOneSeasonBar is one season whose odd and even cycles differ
// between the per-season bar and the single-season bar: a band with only
// that season compared must not fail on it alone.
func TestParityOneSeasonBar(t *testing.T) {
	t.Parallel()
	const P, amp = 1.9171, 0.05
	found := false
	for k := 1; k <= 60 && !found; k++ {
		share := 0.01 * float64(k)
		rng := lcg(2828)
		pts := make([]obs, 0, 90)
		for i := range 90 {
			tt := 2458400 + float64(i)*1.1 + 0.3*rng.next()
			m := amp*math.Cos(2*math.Pi*(tt-2459000.25)/P) + share*amp*math.Cos(math.Pi*(tt-2459000.25)/P)
			pts = append(pts, obs{t: tt, y: m + 0.01*rng.gauss(), e: 0.01})
		}
		many, ok := parityDiffers(pts, P, 2459000.25, amp, parityChi2)
		one, _ := parityDiffers(pts, P, 2459000.25, amp, parityOneChi2)
		if ok && many && !one {
			found = true
		}
	}
	if !found {
		t.Error("no season differs at the per-season bar but not at the single-season bar")
	}
}
