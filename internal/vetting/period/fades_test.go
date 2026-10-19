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

func TestFadesFlatNeighbourStaysFlat(t *testing.T) {
	t.Parallel()
	_, comps := caseSeries(t, fades15523)
	for i := range 5 {
		others := append(append([][]lc.Series{}, comps[:i]...), comps[i+1:]...)
		r, _ := Analyze(comps[i], Options{ChangeMag: 0.21, Neighbours: func() [][]lc.Series { return others }})
		z := rows(r)[check.ZTF]
		if r.Fades != nil || !strings.HasPrefix(z.Evidence, "Flat: ") {
			t.Errorf("comparison %d: ZTF row %+v fades %+v, want flat", i+1, z, r.Fades)
		}
	}
}

// fadeField is a flat star and 30 neighbours over 600 nights, the star
// fading 0.3 mag in two exposures on each of four nights; with shared the
// neighbours fade in those exposures too.
func fadeField(shared bool) ([]lc.Series, [][]lc.Series) {
	rng := lcg(4242)
	dips := map[int]bool{100: true, 230: true, 371: true, 512: true}
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
