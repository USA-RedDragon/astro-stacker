package eclfacts

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

const (
	ztfR   = "ZTF r"
	atlasO = "ATLAS o"
)

func ephem(phase2, d1, d2 float64) *period.Ephemeris {
	return &period.Ephemeris{Period: 3.5, PeriodErr: 1e-5, T0: 2459000.3, T0Err: 0.001, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.03, Shape: 1.5, Depth: map[string]float64{ztfR: d1}},
			{Phase: phase2, Width: 0.03, Shape: 1.5, Depth: map[string]float64{ztfR: d2}},
		}}
}

// sample makes a ZTF r light curve of e with n points and noise sigma.
func sample(e *period.Ephemeris, n int, sigma float64, seed uint64) lc.Series {
	rng := rand.New(rand.NewPCG(seed, 1))
	s := lc.Series{Survey: lc.ZTF, Band: "r"}
	for i := range n {
		t := 2458300 + float64(i)*1.37 + 0.2*rng.Float64()
		s.Points = append(s.Points, lc.Point{BJD: t, Mag: 15 + e.Predict(t, ztfR) + sigma*rng.NormFloat64(), Err: sigma})
	}
	return s
}

func TestTwinTellsUnequalDips(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.4, 0.2)
	s := Summarize([]lc.Series{sample(e, 1500, 0.02, 1)}, e)
	if s == nil || s.Twin == nil {
		t.Fatal("no twin test")
	}
	if s.Twin.DepthZ < 5 {
		t.Errorf("depths 0.4 and 0.2 differ by only %.1f sigma", s.Twin.DepthZ)
	}
	if math.Abs(s.Twin.Depth1.Value-0.4) > 0.03 || math.Abs(s.Twin.Depth2.Value-0.2) > 0.03 {
		t.Errorf("depths %+v %+v, want 0.4 and 0.2", s.Twin.Depth1, s.Twin.Depth2)
	}
	if s.Twin.PhaseZ > 4 {
		t.Errorf("a dip at mid-cycle is %.1f sigma off it", s.Twin.PhaseZ)
	}
}

func TestTwinFindsEqualDipsAlike(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.3, 0.3)
	s := Summarize([]lc.Series{sample(e, 1500, 0.02, 2)}, e)
	if s.Twin.DepthZ >= 3 || s.Twin.PhaseZ >= 3 {
		t.Errorf("equal dips at mid-cycle: depth %.1f sigma, phase %.1f sigma apart", s.Twin.DepthZ, s.Twin.PhaseZ)
	}
}

// TestTwinNeedsTwoBands is the case of source 1688: equal dips in ZTF r,
// and a second band whose dips differ by a few sigma from its own
// systematics. With two bands measured, one band alone cannot make the
// dips differ.
func TestTwinNeedsTwoBands(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.3, 0.3)
	r := sample(e, 1500, 0.02, 4)
	odd := ephem(0.5, 0.36, 0.18)
	odd.Eclipses[0].Depth = map[string]float64{atlasO: 0.36}
	odd.Eclipses[1].Depth = map[string]float64{atlasO: 0.18}
	rng := rand.New(rand.NewPCG(5, 1))
	o := lc.Series{Survey: lc.ATLAS, Band: "o"}
	for i := range 1500 {
		tt := 2458300 + float64(i)*1.29 + 0.2*rng.Float64()
		o.Points = append(o.Points, lc.Point{BJD: tt, Mag: 15 + odd.Predict(tt, atlasO) + 0.03*rng.NormFloat64(), Err: 0.03})
	}
	s := Summarize([]lc.Series{r, o}, e)
	if s.Twin.DepthZ >= 3 {
		t.Errorf("equal dips in r, unequal only in ATLAS o: %.1f sigma apart in %s", s.Twin.DepthZ, s.Twin.Band)
	}
	g := sample(ephem(0.5, 0.4, 0.2), 1500, 0.02, 6)
	g.Band = "g"
	s = Summarize([]lc.Series{sample(ephem(0.5, 0.4, 0.2), 1500, 0.02, 7), g}, ephem(0.5, 0.4, 0.2))
	if s.Twin.DepthZ < 5 {
		t.Errorf("unequal dips in r and g: only %.1f sigma apart", s.Twin.DepthZ)
	}
}

func TestTwinTellsOffCentreDip(t *testing.T) {
	t.Parallel()
	e := ephem(0.52, 0.3, 0.3)
	s := Summarize([]lc.Series{sample(e, 2000, 0.02, 3)}, e)
	if s.Twin.PhaseZ < 5 {
		t.Errorf("a dip at 0.52 is only %.1f sigma from mid-cycle (%.4f +- %.4f)", s.Twin.PhaseZ, s.Twin.Phase2, s.Twin.PhaseErr)
	}
	if math.Abs(s.Twin.Phase2-0.52) > 0.003 {
		t.Errorf("second dip at %.4f, want 0.52", s.Twin.Phase2)
	}
}

func TestCoverageCountsPointsAndEclipses(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.3, 0.1)
	in := e.T0 + 7*e.Period
	s := lc.Series{Survey: lc.ZTF, Band: "r", Points: []lc.Point{
		{BJD: in, Mag: 15.3, Err: 0.02},
		{BJD: in + 0.01, Mag: 15.3, Err: 0.02},
		{BJD: in + 2*e.Period, Mag: 15.3, Err: 0.02},
		{BJD: in + 0.5*e.Period, Mag: 15.1, Err: 0.02},
		{BJD: in + 0.25*e.Period, Mag: 15, Err: 0.02},
	}}
	tess := lc.Series{Survey: lc.TESS, Band: "S20", Relative: true, Points: []lc.Point{{BJD: in + 5*e.Period, Mag: 0.3, Err: 0.001}, {BJD: in + 5*e.Period + 0.001, Mag: 0.3, Err: 0.001}}}
	c := coverage(points([]lc.Series{s, tess}, e), e)
	if c[0].Points != 3 || c[0].Events != 3 {
		t.Errorf("deep dip %+v, want 3 points (TESS not counted) and 3 eclipses", c[0])
	}
	if c[1].Points != 1 || c[1].Events != 1 {
		t.Errorf("second dip %+v, want 1 point in 1 eclipse", c[1])
	}
}

func TestNormalIsMedianOutsideEclipses(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.5, 0.2)
	s := sample(e, 600, 0.01, 4)
	n := normal([]lc.Series{s}, e)
	if n == nil || n.Band != "r" || math.Abs(n.Mag-15) > 0.005 {
		t.Fatalf("normal %+v, want r 15.00", n)
	}
	g := lc.Series{Survey: lc.ZTF, Band: "g", Points: s.Points}
	short := lc.Series{Survey: lc.ZTF, Band: "r", Points: s.Points[:5]}
	if n := normal([]lc.Series{g, short}, e); n == nil || n.Band != "g" {
		t.Errorf("with too few r points, normal %+v, want g", n)
	}
}

// TestTwoFieldsOneZeroPoint merges a ZTF field 0.15 mag fainter into the
// light curve, its 180 points mostly inside the dips: the depths and the
// normal brightness stay those of the main field.
func TestTwoFieldsOneZeroPoint(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.4, 0.2)
	s := sample(e, 1500, 0.02, 9)
	for i := range s.Points {
		s.Points[i].Src = 1
	}
	rng := rand.New(rand.NewPCG(10, 1))
	for i := range 200 {
		ph := 0.04*rng.Float64() - 0.02
		switch {
		case i%10 == 0:
			ph = 0.25
		case i%2 == 1:
			ph += 0.5
		}
		tt := e.T0 + float64(i*3-300)*e.Period + (ph+0.002*rng.NormFloat64())*e.Period
		s.Points = append(s.Points, lc.Point{BJD: tt, Mag: 15.15 + e.Predict(tt, ztfR) + 0.02*rng.NormFloat64(), Err: 0.02, Src: 2})
	}
	one := s
	one.Points = slices.Clone(s.Points)
	for i := range one.Points {
		one.Points[i].Src = 0
	}
	bad := Summarize([]lc.Series{one}, e)
	t.Logf("one zero point: depths %.3f %.3f, normal %.3f", bad.Twin.Depth1.Value, bad.Twin.Depth2.Value, bad.Normal.Mag)
	if math.Abs(bad.Twin.Depth1.Value-0.4) < 0.03 {
		t.Error("on one zero point the depth is right too; the test shows nothing")
	}
	got := Summarize([]lc.Series{s}, e)
	if math.Abs(got.Twin.Depth1.Value-0.4) > 0.01 || math.Abs(got.Twin.Depth2.Value-0.2) > 0.01 {
		t.Errorf("depths %+v %+v, want 0.4 and 0.2", got.Twin.Depth1, got.Twin.Depth2)
	}
	if got.Normal == nil || math.Abs(got.Normal.Mag-15) > 0.003 {
		t.Errorf("normal %+v, want 15.00", got.Normal)
	}
}

func TestSecondaryLimitOnOneDip(t *testing.T) {
	t.Parallel()
	e := &period.Ephemeris{Period: 3.5, T0: 2459000.3, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.03, Shape: 1.5, Depth: map[string]float64{ztfR: 0.3}}}}
	s := Summarize([]lc.Series{sample(e, 1500, 0.02, 5)}, e)
	if s.Secondary == nil || s.Secondary.Band != "r" || s.Secondary.Mag <= 0 || s.Secondary.Mag > 0.05 {
		t.Errorf("secondary limit %+v, want a small r limit", s.Secondary)
	}
	if d := Doubled(e); d == nil || d.Period != 7 || len(d.Eclipses) != 2 || d.Eclipses[1].Phase != 0.5 || d.Eclipses[0].Width != 0.015 {
		t.Errorf("doubled %+v", d)
	}
}

func TestDurationAtFivePercent(t *testing.T) {
	t.Parallel()
	e := ephem(0.5, 0.3, 0.3)
	e.Eclipses[0].Shape = 1
	want := 2 * 0.03 * math.Sqrt(0.95) * 3.5
	if got := Duration(e, 0); math.Abs(got-want) > 1e-12 {
		t.Errorf("duration %.6f, want %.6f", got, want)
	}
}
