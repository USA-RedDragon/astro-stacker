package period

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func TestOneDipAlikeOnOddAndEvenLeavesTwiceOpen(t *testing.T) {
	t.Parallel()
	const P, t0 = 3.21, 2459000.3
	rng := lcg(11)
	g := lc.Series{Survey: lc.ZTF, Band: "g", Dilution: 1}
	r := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
	model := func(tt, d float64) float64 { return d * shapeOnly(wrapPhase((tt-t0)/P), 0.04, 1.5) }
	for night := range 1400 {
		tt := 2458300 + float64(night)*1.7 + 0.3*rng.next()
		g.Points = append(g.Points, lc.Point{BJD: tt, Mag: 16 + model(tt, 0.4) + 0.02*rng.gauss(), Err: 0.02})
		r.Points = append(r.Points, lc.Point{BJD: tt + 0.02, Mag: 15.6 + model(tt+0.02, 0.35) + 0.02*rng.gauss(), Err: 0.02})
	}
	res, err := Analyze([]lc.Series{g, r}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Ephemeris
	if e.Kind != KindEclipsing || len(e.Eclipses) != 1 || math.Abs(e.Period-P) > 3*e.PeriodErr+1e-5 {
		t.Fatalf("got %s with %d dips at %.6f", e.Kind, len(e.Eclipses), e.Period)
	}
	d := rows(res)[check.Double]
	if d.Status != check.NotRun || !strings.HasPrefix(d.Evidence, SameDepthOddEven) {
		t.Errorf("double check %+v, want undecided: two equal dips at twice the period look the same", d)
	}
}
