package period

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// fades15523 is the ZTF light curve of 15523 (Gaia DR3
// 1859704209061192832, Cygnus Loop Panel 1), a dipper found by a hand
// audit, with the 30 stars within 3 arcmin closest to it in G, from the
// field's ZTF files.
const fades15523 = "testdata/fades/15523.csv.zst"

func TestFadesConfirmDipper(t *testing.T) {
	t.Parallel()
	target, comps := caseSeries(t, fades15523)
	r, err := Analyze(target, Options{ChangeMag: 0.21, Neighbours: func() [][]lc.Series { return comps }})
	if !errors.Is(err, ErrNoPeriod) {
		t.Fatalf("err %v, want no period", err)
	}
	z := rows(r)[check.ZTF]
	t.Logf("%+v %+v", z, r.Fades)
	if !r.Fades.Confirms() || z.Status != check.Pass || !strings.HasPrefix(z.Evidence, "Fades on ") {
		t.Errorf("ZTF row %+v fades %+v, want the fades confirmed", z, r.Fades)
	}
}

func TestFadesNeighboursNotConfirmed(t *testing.T) {
	t.Parallel()
	_, comps := caseSeries(t, fades15523)
	for i := range comps {
		others := append(append([][]lc.Series{}, comps[:i]...), comps[i+1:]...)
		for _, c := range []float64{0.06, 0.1, -0.1, 0.21, -0.21} {
			r, _ := Analyze(comps[i], Options{ChangeMag: c, Neighbours: func() [][]lc.Series { return others }})
			if r.Fades.Confirms() {
				t.Errorf("comparison %d at %.2f: fades %+v, ZTF row %+v", i+1, c, r.Fades, rows(r)[check.ZTF])
			}
		}
	}
	r, _ := Analyze(comps[0], Options{ChangeMag: 0.21, Neighbours: func() [][]lc.Series { return comps[1:] }})
	if z := rows(r)[check.ZTF]; r.Fades != nil || !strings.HasPrefix(z.Evidence, "Flat: ") {
		t.Errorf("comparison 1: ZTF row %+v fades %+v, want flat", z, r.Fades)
	}
}

// noisyStar is a steady star of scatter sigma seen in four exposures a
// night over 400 nights, with 30 neighbours like it; off moves one small
// light curve of its own (lc.Point.Src 2), eight points on four nights,
// that much fainter.
func noisyStar(seed uint64, sigma, off float64) ([]lc.Series, [][]lc.Series) {
	rng := lcg(seed)
	var times []float64
	for n := range 400 {
		for k := range 4 {
			times = append(times, 2458300+float64(n)+0.7+0.02*float64(k))
		}
	}
	mk := func(shift float64) []lc.Series {
		s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
		for i, t := range times {
			src, m := int64(1), 16.9+sigma*rng.gauss()
			if n := i / 4; n%97 == 5 && i%4 < 2 {
				src, m = 2, m+shift
			}
			s.Points = append(s.Points, lc.Point{BJD: t, Mag: m, Err: sigma, Src: src})
		}
		return []lc.Series{s}
	}
	target := mk(off)
	comps := make([][]lc.Series, 30)
	for i := range comps {
		comps[i] = mk(0)
	}
	return target, comps
}

func TestFadesNoiseNotConfirmed(t *testing.T) {
	t.Parallel()
	for seed := range uint64(20) {
		for _, c := range []struct{ sigma, off, c float64 }{{0.03, 0, 0.1}, {0.03, 0, 0.06}, {0.02, 0, 0.1}, {0.015, 0.1, 0.15}, {0.015, 0.2, 0.15}} {
			target, comps := noisyStar(seed+1, c.sigma, c.off)
			r, _ := Analyze(target, Options{ChangeMag: c.c, Neighbours: func() [][]lc.Series { return comps }})
			if r.Fades.Confirms() {
				t.Errorf("seed %d %+v: fades %+v, ZTF row %+v", seed, c, r.Fades, rows(r)[check.ZTF])
			}
		}
	}
}

// fadeField is a flat star and 30 neighbours over 600 nights, the star
// fading 0.3 mag in two exposures on each of four nights; with shared the
// neighbours fade in those exposures too.
func fadeField(shared bool) ([]lc.Series, [][]lc.Series) {
	return fadeFieldOn(shared, 100, 230, 371, 512)
}

// fadeFieldOn is fadeField with the star fading on the given nights only.
func fadeFieldOn(shared bool, nights ...int) ([]lc.Series, [][]lc.Series) {
	rng := lcg(4242)
	dips := map[int]bool{}
	for _, n := range nights {
		dips[n] = true
	}
	var times []float64
	for n := range 600 {
		base := 2458300.0 + float64(n) + 0.75 + 0.02*rng.gauss()
		times = append(times, base)
		if dips[n] {
			times = append(times, base+0.03)
		}
	}
	dipAt := func(t float64) bool { return dips[int(math.Floor(t-2458300))] }
	mk := func(mag float64, deep bool) []lc.Series {
		s := lc.Series{Survey: lc.ZTF, Band: "r", Dilution: 1}
		for _, t := range times {
			m := mag + 0.02*rng.gauss()
			if deep && dipAt(t) {
				m += 0.3
			}
			s.Points = append(s.Points, lc.Point{BJD: t, Mag: m, Err: 0.02, Src: 1})
		}
		return []lc.Series{s}
	}
	target := mk(16.9, true)
	comps := make([][]lc.Series, 30)
	for i := range comps {
		comps[i] = mk(16.4+float64(i)/30, shared)
	}
	return target, comps
}

func TestFadesField(t *testing.T) {
	t.Parallel()
	for _, shared := range []bool{false, true} {
		target, comps := fadeField(shared)
		r, err := Analyze(target, Options{ChangeMag: 0.3, Neighbours: func() [][]lc.Series { return comps }})
		if !errors.Is(err, ErrNoPeriod) {
			t.Fatalf("shared %v: err %v, want no period", shared, err)
		}
		z := rows(r)[check.ZTF]
		t.Logf("shared %v: %+v %+v", shared, z, r.Fades)
		switch {
		case shared && (!r.Fades.FieldProblem() || z.Status != check.Fail || !strings.HasPrefix(z.Evidence, "Nearby stars fade with it")):
			t.Errorf("neighbours fade too: ZTF row %+v fades %+v, want a field problem", z, r.Fades)
		case !shared && (!r.Fades.Confirms() || r.Fades.Steady != 4 || z.Status != check.Pass):
			t.Errorf("neighbours steady: ZTF row %+v fades %+v, want 4 nights confirmed", z, r.Fades)
		}
	}
}

// TestSteadyFadeNightsFromOne is the known answer for the watch's fade
// count: one backed ZTF night with nearby stars steady is listed though
// Fades waits for two, and a second adds exactly one more, while the
// verdict's Fades stays as it was.
func TestSteadyFadeNightsFromOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		nights []int
		want   int
		fades  bool
	}{{nil, 0, false}, {[]int{230}, 1, false}, {[]int{230, 371}, 2, true}, {[]int{100, 230, 371, 512}, 4, true}} {
		target, comps := fadeFieldOn(false, c.nights...)
		r, _ := Analyze(target, Options{ChangeMag: 0.3, Neighbours: func() [][]lc.Series { return comps }})
		if len(r.SteadyFadeNights) != c.want || (r.Fades != nil) != c.fades || (c.fades && r.Fades.Steady != c.want) {
			t.Errorf("nights %v: steady nights %v fades %+v, want %d listed", c.nights, r.SteadyFadeNights, r.Fades, c.want)
		}
		for i, bjd := range r.SteadyFadeNights {
			if math.Floor(bjd-2458300) != float64(c.nights[i]) {
				t.Errorf("nights %v: listed %v", c.nights, r.SteadyFadeNights)
			}
		}
	}
	target, comps := fadeFieldOn(true, 230)
	r, _ := Analyze(target, Options{ChangeMag: 0.3, Neighbours: func() [][]lc.Series { return comps }})
	if len(r.SteadyFadeNights) != 0 || r.Fades != nil {
		t.Errorf("a lone night the nearby stars fade on too: %v %+v, want none", r.SteadyFadeNights, r.Fades)
	}
}
