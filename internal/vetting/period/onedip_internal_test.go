package period

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func oneDipStar(gDepth, rDepth float64) []lc.Series {
	const P, t0 = 3.21, 2459000.3
	rng := lcg(11)
	g := lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
	r := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	model := func(tt, d float64) float64 { return d * shapeOnly(wrapPhase((tt-t0)/P), 0.04, 1.5) }
	for night := range 1400 {
		tt := 2458300 + float64(night)*1.7 + 0.3*rng.next()
		g.Points = append(g.Points, lc.Point{BJD: tt, Mag: 16 + model(tt, gDepth) + 0.02*rng.gauss(), Err: 0.02})
		r.Points = append(r.Points, lc.Point{BJD: tt + 0.02, Mag: 15.6 + model(tt+0.02, rDepth) + 0.02*rng.gauss(), Err: 0.02})
	}
	return []lc.Series{g, r}
}

// TestShallowOneDipLeavesTwiceOpen is one 0.15 mag dip per cycle, alike on
// odd and even cycles: a red or brown dwarf can block that much light and
// stay dark, so the period stands and twice it stays open.
func TestShallowOneDipLeavesTwiceOpen(t *testing.T) {
	t.Parallel()
	const P = 3.21
	res, err := Analyze(oneDipStar(0.15, 0.13), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 1 || math.Abs(e.Period-P) > 3*e.PeriodErr+1e-5 {
		t.Fatalf("got %s with %d dips at %.6f", e.Kind, len(e.Eclipses), e.Period)
	}
	d := rows(res)[check.Double]
	if d.Status != check.NotRun || !strings.HasPrefix(d.Evidence, SameDepthOddEven) || !strings.Contains(d.Evidence, "dark enough to hide") {
		t.Errorf("double check %+v, want undecided with the dark companion rule named", d)
	}
}

// TestDeepOneDipIsTwoEqualEclipses is one 0.4 mag dip per cycle with
// nothing half a cycle later: no companion that dark can block 30% of the
// light, so the orbit is twice the period with two equal eclipses.
func TestDeepOneDipIsTwoEqualEclipses(t *testing.T) {
	t.Parallel()
	const P = 3.21
	res, err := Analyze(oneDipStar(0.4, 0.35), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 2 || math.Abs(e.Period-2*P) > 3*e.PeriodErr+2e-5 {
		t.Fatalf("got %s with %d dips at %.6f, want two at %.4f", e.Kind, len(e.Eclipses), e.Period, 2*P)
	}
	if ph := e.Eclipses[1].Phase; math.Abs(ph-0.5) > 0.002 {
		t.Errorf("second eclipse at phase %.4f, want 0.5", ph)
	}
	h := rows(res)[check.Half]
	if h.Status != check.Pass || !strings.HasPrefix(h.Evidence, NoDarkCompanion) {
		t.Errorf("half check %+v, want passed by the dark companion rule", h)
	}
}
