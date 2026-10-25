package vetting

import (
	"encoding/csv"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

// star299 is Barnard's E 299, Gaia DR3 4315019506223807488 at G 15.50,
// with Gaia DR3 4315019506169279744 at G 16.35 2.05" away.
func star299() surveys.Target {
	return surveys.Target{
		GaiaID: 4315019506223807488, RA: 293.8656436069509, Dec: 11.04858633330574,
		PMRA: -2.7880292598002487, PMDec: -6.1470070361105815, G: 15.497252,
		Neighbours: []surveys.Neighbour{{GaiaID: 4315019506169279744, RA: 293.86572719390824, Dec: 11.048021592861026, G: 16.353117, Sep: 2.054}},
	}
}

// ztf299 is the light curve service's answer at 299 in one band, its
// catflags 0 epochs with the position ZTF measured on each.
func ztf299(t *testing.T, band string) lc.Series {
	t.Helper()
	return ztfAPI(t, "299", band)
}

// ztfAPI reads a light curve service answer kept in testdata for one
// source and band, its catflags 0 epochs with the position ZTF measured
// on each.
func ztfAPI(t *testing.T, source, band string) lc.Series {
	t.Helper()
	raw, err := os.ReadFile("testdata/ztf_api_" + source + "_" + band + ".csv.zst")
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(string(text))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}
	num := func(r []string, c string) float64 {
		v, err := strconv.ParseFloat(r[col[c]], 64)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	s := lc.Series{Survey: lc.ZTF, Band: band}
	for _, r := range recs[1:] {
		if r[col["catflags"]] != "0" {
			continue
		}
		oid, err := strconv.ParseInt(r[col["oid"]], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		s.Points = append(s.Points, lc.Point{BJD: num(r, "hjd"), Mag: num(r, "mag"), Err: num(r, "magerr"), Src: oid, RA: num(r, "ra"), Dec: num(r, "dec")})
	}
	return s
}

// TestPairShift299 is 299 in ZTF: the object brightens toward the G 16.35
// neighbour by 1.33 +- 0.14" per magnitude in r and, with one g point far
// off dropped, 1.26 +- 0.22" in g (0.91 +- 0.27" with it), as a changing
// share of the neighbour's light does (1.30") and the star changing
// (0.59" away) cannot.
func TestPairShift299(t *testing.T) {
	t.Parallel()
	ss := []lc.Series{ztf299(t, "g"), ztf299(t, "r")}
	if len(ss[1].Points) != 62 || len(ss[0].Points) != 41 {
		t.Fatalf("%d g and %d r points, want 41 and 62", len(ss[0].Points), len(ss[1].Points))
	}
	for _, s := range ss {
		p, ok := pairShiftOf(star299(), []lc.Series{s})
		if !ok {
			t.Fatalf("no shift in %s", s.Band)
		}
		t.Logf("%s: %.3f ± %.3f\"/mag over %d points; share %.3f, own %.3f", s.Band, p.slope, p.err, p.points, p.share, p.own)
		want, werr := -1.326, 0.143
		if s.Band == "g" {
			want, werr = -1.257, 0.224
		}
		if math.Abs(p.slope-want) > 0.01 || math.Abs(p.err-werr) > 0.01 {
			t.Errorf("%s: %.3f ± %.3f, want %.3f ± %.3f", s.Band, p.slope, p.err, want, werr)
		}
	}
	p, ok := pairShiftOf(star299(), ss)
	if !ok {
		t.Fatal("no shift")
	}
	if math.Abs(p.share+1.301) > 0.005 || math.Abs(p.own-0.591) > 0.005 {
		t.Errorf("predicted %.3f for the neighbour's share and %.3f for the star, want -1.301 and 0.591", p.share, p.own)
	}
	if !p.refutes() || p.supports() {
		t.Errorf("%.3f ± %.3f: refutes %v, supports %v", p.slope, p.err, p.refutes(), p.supports())
	}
}

// TestPairVaries299 runs the period search on 299's ZTF light curves: it
// finds the star irregular and its ZTF row says it varies, which the
// shift toward the neighbour turns to a fail.
func TestPairVaries299(t *testing.T) {
	t.Parallel()
	ss := []lc.Series{ztf299(t, "g"), ztf299(t, "r")}
	tg := star299()
	for i := range ss {
		ss[i].Dilution = 1 / (1 + math.Pow(10, -0.4*(tg.Neighbours[0].G-tg.G)))
	}
	pr, err := period.Analyze(ss, period.Options{ChangeMag: 0.09, G: tg.G})
	if err != nil && pr == nil {
		t.Fatal(err)
	}
	ztf := func() check.Check {
		for _, c := range pr.Checks {
			if c.ID == check.ZTF {
				return c
			}
		}
		t.Fatal("no ZTF row")
		return check.Check{}
	}
	if !pr.Irregular || ztf().Status != check.Pass || !strings.HasPrefix(ztf().Evidence, "Varies:") {
		t.Fatalf("irregular %v, ZTF row %+v: want the stored Varies pass", pr.Irregular, ztf())
	}
	pairVaries(pr, tg, ss)
	c := ztf()
	t.Log(c.Evidence)
	if c.Status != check.Fail || !strings.Contains(c.Evidence, "not the star's own") || !strings.Contains(c.Evidence, "toward it") {
		t.Errorf("ZTF row %+v", c)
	}
}

// pairCurve makes ZTF points of a star and its neighbour measured as one
// object: the star's light changes by own magnitudes and the neighbour's
// by share, and the object sits at their light-weighted centre with
// noise in arcsec.
func pairCurve(tg surveys.Target, own, share func(i int) float64, n int, noise float64, seed uint64) lc.Series {
	rng := rand.New(rand.NewPCG(seed, 7))
	nb := tg.Neighbours[0]
	cosd := math.Cos(tg.Dec * math.Pi / 180)
	ft0, fn0 := math.Pow(10, -0.4*tg.G), math.Pow(10, -0.4*nb.G)
	s := lc.Series{Survey: lc.ZTF, Band: "r"}
	for i := range n {
		ft, fn := ft0*math.Pow(10, -0.4*own(i)), fn0*math.Pow(10, -0.4*share(i))
		w := fn / (ft + fn)
		ra := tg.RA + w*(nb.RA-tg.RA) + rng.NormFloat64()*noise/3600/cosd
		dec := tg.Dec + w*(nb.Dec-tg.Dec) + rng.NormFloat64()*noise/3600
		mag := -2.5*math.Log10(ft+fn) + rng.NormFloat64()*0.015
		s.Points = append(s.Points, lc.Point{BJD: 2458300 + float64(i)*3.1, Mag: mag, Err: 0.015, Src: 1, RA: ra, Dec: dec})
	}
	return s
}

// TestPairShiftRealVariable is the negative control: a star in 299's pair
// that itself varies by 0.3 mag must not be refuted, and its shift away
// from the neighbour is named as its own.
func TestPairShiftRealVariable(t *testing.T) {
	t.Parallel()
	tg := star299()
	rng := rand.New(rand.NewPCG(3, 4))
	amp := make([]float64, 80)
	for i := range amp {
		amp[i] = 0.3 * rng.Float64()
	}
	s := pairCurve(tg, func(i int) float64 { return amp[i] }, func(int) float64 { return 0 }, 80, 0.08, 1)
	p, ok := pairShiftOf(tg, []lc.Series{s})
	if !ok {
		t.Fatal("no shift")
	}
	t.Logf("%.3f ± %.3f\"/mag; share %.3f, own %.3f", p.slope, p.err, p.share, p.own)
	if p.refutes() || !p.supports() {
		t.Errorf("refutes %v, supports %v", p.refutes(), p.supports())
	}
	pr := &period.Result{Irregular: true, Checks: []check.Check{check.New(check.ZTF, check.Pass, "Varies: 80 r points.")}}
	pairVaries(pr, tg, []lc.Series{s})
	if c := pr.Checks[0]; c.Status != check.Pass || !strings.Contains(c.Evidence, "The move is the star's own.") {
		t.Errorf("ZTF row %+v", c)
	}
}

// TestPairShiftNeighbourVaries is the neighbour varying by 0.5 mag while
// the star stays steady: the change is refuted as the star's.
func TestPairShiftNeighbourVaries(t *testing.T) {
	t.Parallel()
	tg := star299()
	rng := rand.New(rand.NewPCG(5, 6))
	amp := make([]float64, 80)
	for i := range amp {
		amp[i] = 0.5 * rng.Float64()
	}
	s := pairCurve(tg, func(int) float64 { return 0 }, func(i int) float64 { return amp[i] }, 80, 0.08, 2)
	p, ok := pairShiftOf(tg, []lc.Series{s})
	if !ok || !p.refutes() || p.supports() {
		t.Errorf("%+v %v", p, ok)
	}
}

// TestPairShiftNoPositions leaves the row alone when ZTF gave no positions,
// as with a cache from before they were kept, or the star has no close
// neighbour.
func TestPairShiftNoPositions(t *testing.T) {
	t.Parallel()
	ss := []lc.Series{ztf299(t, "r")}
	for i := range ss[0].Points {
		ss[0].Points[i].RA, ss[0].Points[i].Dec = 0, 0
	}
	if _, ok := pairShiftOf(star299(), ss); ok {
		t.Error("a shift without positions")
	}
	lone := star299()
	lone.Neighbours[0].Sep, lone.Neighbours[0].RA = 0, lone.RA+0.002
	if _, ok := pairShiftOf(lone, []lc.Series{ztf299(t, "r")}); ok {
		t.Error("a shift for a star with no close neighbour")
	}
	pr := &period.Result{Irregular: true, Checks: []check.Check{check.New(check.ZTF, check.Pass, "Varies.")}}
	pairVaries(pr, star299(), ss)
	if pr.Checks[0].Status != check.Pass || pr.Checks[0].Evidence != "Varies." {
		t.Errorf("%+v", pr.Checks[0])
	}
}
