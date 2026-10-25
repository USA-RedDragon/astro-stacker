package vetting

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

const (
	gaia70878A = 1858173345273104256
	gaia70878B = 1858173349582316032
)

// stars70878 are the Gaia DR3 rows at 70878 (Cygnus Loop, 2026-10-25):
// the G 15.46 star the spot was matched to, with no parallax or proper
// motion, and the G 15.88 star 0.85" from it.
func stars70878() []starcat.GaiaStar {
	return []starcat.GaiaStar{
		{SourceID: gaia70878A, RA: 312.37546665179, Dec: 29.05638895828, G: 15.457724, BP: 15.83, RP: 14.92,
			PMRA: math.NaN(), PMDec: math.NaN(), Parallax: math.NaN()},
		{SourceID: gaia70878B, RA: 312.37529983734, Dec: 29.05657462613, G: 15.884327, BP: 16.24, RP: 15.37,
			PMRA: -0.193, PMDec: -1.631, Parallax: 0.3734, ParallaxError: 0.05},
	}
}

// ephemeris70878 is the stored vet's ZTF ephemeris at 70878, its depths
// undiluted for the G 15.46 star.
func ephemeris70878() *period.Ephemeris {
	return &period.Ephemeris{Period: 9.096889225114335, T0: 2459066.778630674, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{
		{Phase: 0, Width: 0.023147951815415888, Shape: 8, Depth: map[string]float64{"ZTF g": 0.4338942450314148, ztfR: 0.46322846781136606}},
		{Phase: 0.3009587870393177, Width: 0.021499421018785866, Shape: 5.442969455902474, Depth: map[string]float64{"ZTF g": 0.3605298490447281, ztfR: 0.3846286940178936}},
	}}
}

// ztf70878 is ZTF g and r at 70878 as fetched for the star t.
func ztf70878(t *testing.T, tg surveys.Target) []lc.Series {
	t.Helper()
	ss := make([]lc.Series, 0, 2)
	for _, b := range []string{"g", "r"} {
		s := ztfAPI(t, "70878", b)
		ss = append(ss, surveys.Rediluted(s, surveys.Target{}, tg))
	}
	return ss
}

// TestPairPick70878 is 70878 in ZTF: in the dips the position of the
// pair moves away from the G 15.88 star by 0.49 +- 0.04" per unit of q in
// r, as that star dimming does (0.51") and the G 15.46 star dimming
// (0.34" the other way) cannot, so the dips are the G 15.88 star's.
func TestPairPick70878(t *testing.T) {
	t.Parallel()
	a := starOf(stars70878(), 0)
	ss := ztf70878(t, a.target)
	if math.Abs(ss[1].Dilution-0.5970) > 0.001 {
		t.Fatalf("ZTF dilution for the G 15.46 star %.4f, want 0.5970", ss[1].Dilution)
	}
	r, ok := pairPickOf(a.target, ss[1:], ephemeris70878())
	if !ok {
		t.Fatal("no pick in r")
	}
	t.Logf("r: %.3f ± %.3f; theirs %.3f, mine %.3f; %d points, %d in dips", r.move, r.err, r.theirs, r.mine, r.points, r.inDip)
	if math.Abs(r.move-0.494) > 0.01 || math.Abs(r.err-0.037) > 0.005 || math.Abs(r.theirs-0.507) > 0.005 || math.Abs(r.mine+0.343) > 0.005 {
		t.Errorf("r: %.3f ± %.3f, theirs %.3f, mine %.3f; want 0.494 ± 0.037, 0.507, -0.343", r.move, r.err, r.theirs, r.mine)
	}
	p, ok := pairPickOf(a.target, ss, ephemeris70878())
	if !ok || !p.theirsVaries() || p.mineVaries() {
		t.Fatalf("pick %v: theirs %v, mine %v", ok, p.theirsVaries(), p.mineVaries())
	}
	if v := -p.move * p.deep; math.Abs(v-0.13) > 0.01 {
		t.Errorf("move at the deeper dip %.3f\", want 0.13\" away from the G 15.88 star", v)
	}
	w := p.words()
	t.Log(w)
	if !strings.Contains(w, "Gaia DR3 1858173349582316032") || !strings.Contains(w, "moves 0.13\" toward the G 15.46 star") {
		t.Errorf("words %q", w)
	}
}

// TestPairPick70878Depths moves 70878's vet to the G 15.88 star and fits
// the stored ephemeris to ZTF again: undiluted for the G 15.46 star the
// r depths are 0.46 and 0.38, as the stored vet said; for the G 15.88
// star they are about 0.8 and 0.6 from the combined 0.255 and 0.212.
func TestPairPick70878Depths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := &starcat.MemBucket{}
	if err := starcat.WriteGaia(ctx, b, "gaia", stars70878()); err != nil {
		t.Fatal(err)
	}
	s := &Service{Gaia: starcat.NewGaia(b, "gaia"), Now: func() time.Time { return time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) }}
	ra, dec := 312.37552421425517, 29.05656400579651
	a, err := s.identify(ctx, ra, dec, 15.46)
	if err != nil || a.target.GaiaID != gaia70878A {
		t.Fatalf("spot matched to %d (%v), want the G 15.46 star", a.target.GaiaID, err)
	}
	fx := fetches{ztf: surveys.Fetch{Survey: lc.ZTF, State: surveys.Done, Series: ztf70878(t, a.target)}}
	depths := func(ss []lc.Series) (float64, float64) {
		pr, err := period.AnalyzeAt(ss, ephemeris70878(), period.Options{ChangeMag: 0.25, G: a.target.G})
		if pr == nil || pr.Ephemeris == nil {
			t.Fatalf("no fit: %v", err)
		}
		return pr.Ephemeris.Eclipses[0].Depth[ztfR], pr.Ephemeris.Eclipses[1].Depth[ztfR]
	}
	d1, d2 := depths(fx.ztf.Series)
	t.Logf("G 15.46 star: r depths %.3f and %.3f", d1, d2)
	if math.Abs(d1-0.46) > 0.03 || math.Abs(d2-0.38) > 0.03 {
		t.Errorf("G 15.46 star: r depths %.3f and %.3f, want about 0.46 and 0.38", d1, d2)
	}
	p, ok := pairPickOf(a.target, fx.ztf.Series, ephemeris70878())
	if !ok {
		t.Fatal("no pick")
	}
	nst, moved := s.pairPicked(ctx, surveys.Field{}, ra, dec, a, p, &fx)
	if !moved || nst.target.GaiaID != gaia70878B || math.Abs(nst.target.G-15.884) > 0.001 {
		t.Fatalf("moved %v to %d", moved, nst.target.GaiaID)
	}
	if line := starLine("", nst); line != "Gaia DR3 1858173349582316032 · Gaia G 15.88" {
		t.Errorf("star line %q", line)
	}
	if nst.skipped != nil || math.Abs(nst.plx-0.3734) > 1e-4 || len(nst.target.Neighbours) != 1 || nst.target.Neighbours[0].GaiaID != gaia70878A {
		t.Errorf("moved star %+v", nst)
	}
	for _, sr := range fx.ztf.Series {
		if math.Abs(sr.Dilution-0.4030) > 0.001 || !strings.Contains(sr.Note, "gives 40% of the light") {
			t.Errorf("ZTF %s dilution %.4f, note %q; want 0.4030", sr.Band, sr.Dilution, sr.Note)
		}
	}
	d1, d2 = depths(fx.ztf.Series)
	t.Logf("G 15.88 star: r depths %.3f and %.3f", d1, d2)
	if math.Abs(d1-0.8) > 0.05 || math.Abs(d2-0.62) > 0.05 {
		t.Errorf("G 15.88 star: r depths %.3f and %.3f, want about 0.8 and 0.6", d1, d2)
	}
}

// TestPairPickOwnDips is the negative control: the G 15.46 star of 70878
// itself eclipsing, measured with its neighbour as one object, keeps the
// vet on it and names the dips its own.
func TestPairPickOwnDips(t *testing.T) {
	t.Parallel()
	a := starOf(stars70878(), 0)
	a.target.Neighbours[0].Sep = 0.85
	e := ephemeris70878()
	s := pairCurve(a.target, func(i int) float64 { return e.Predict(2458300+float64(i)*3.1, ztfR) }, func(int) float64 { return 0 }, 2500, 0.06, 9)
	s.Dilution = 0.5970
	p, ok := pairPickOf(a.target, []lc.Series{s}, e)
	if !ok {
		t.Fatal("no pick")
	}
	t.Logf("%.3f ± %.3f; theirs %.3f, mine %.3f; %d in dips", p.move, p.err, p.theirs, p.mine, p.inDip)
	if !p.mineVaries() || p.theirsVaries() || !strings.HasSuffix(p.words(), "The dips are the G 15.46 star's own.") {
		t.Errorf("mine %v, theirs %v: %s", p.mineVaries(), p.theirsVaries(), p.words())
	}
	s2 := &Service{}
	if _, moved := s2.pairPicked(context.Background(), surveys.Field{}, a.target.RA, a.target.Dec, a, p, &fetches{}); moved {
		t.Error("vet moved off a star whose dips are its own")
	}
}

// TestPairPickNeedsPeriod leaves a star with no period alone, as 299,
// whose ZTF position follows the seeing, and a star with no close
// neighbour.
func TestPairPickNeedsPeriod(t *testing.T) {
	t.Parallel()
	a := starOf(stars70878(), 0)
	ss := ztf70878(t, a.target)
	if _, ok := pairPickOf(a.target, ss, nil); ok {
		t.Error("a pick with no period")
	}
	if _, ok := pairPickOf(star299(), []lc.Series{ztf299(t, "r")}, &period.Ephemeris{Kind: period.KindIrregular}); ok {
		t.Error("a pick for an irregular star")
	}
	lone := a.target
	lone.Neighbours = nil
	if _, ok := pairPickOf(lone, ss, ephemeris70878()); ok {
		t.Error("a pick with no neighbour")
	}
}
