package dustcheck_test

import (
	"bytes"
	"image/png"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/dustcheck"
)

const (
	red    = "Red"
	synthW = 2400
	synthH = 1600
)

type disc struct {
	x, y, d int
	depth   float64
}

// synthFlat is a vignetted flat with dust shadows and a little noise.
func synthFlat(seed uint64, discs ...disc) []float32 {
	rng := rand.New(rand.NewPCG(seed, 1))
	pix := make([]float32, synthW*synthH)
	for y := range synthH {
		for x := range synthW {
			dx, dy := float64(x-synthW/2)/synthW, float64(y-synthH/2)/synthW
			v := 0.5 * (1 - 0.08*(dx*dx+dy*dy))
			for _, d := range discs {
				r := math.Hypot(float64(x-d.x), float64(y-d.y))
				if r < float64(d.d)/2 {
					v *= 1 - d.depth
				}
			}
			pix[y*synthW+x] = float32(v * (1 + 0.002*rng.NormFloat64()))
		}
	}
	return pix
}

func flatOf(id int, seed uint64, at time.Time, discs ...disc) dustcheck.Flat {
	m := dustcheck.DustMap(synthFlat(seed, discs...), synthW, synthH)
	return dustcheck.Flat{ID: id, Filter: red, Rotator: 141, TakenAt: at, Map: dustcheck.Prepare(m)}
}

func changes(evs []dustcheck.Event) []dustcheck.Event {
	var out []dustcheck.Event
	for _, e := range evs {
		if dustcheck.Change(e.Class) {
			out = append(out, e)
		}
	}
	return out
}

func near(e dustcheck.Event, x, y int) bool {
	return math.Hypot(float64(e.X-x), float64(e.Y-y)) < 40
}

func TestDustMapFindsShadow(t *testing.T) {
	t.Parallel()
	m := dustcheck.DustMap(synthFlat(1, disc{1200, 800, 400, 0.01}), synthW, synthH)
	var shadow *dustcheck.Blob
	for _, b := range dustcheck.Catalog(dustcheck.Prepare(m)) {
		if near(dustcheck.Event{Blob: b}, 1200, 800) {
			shadow = &b
		}
	}
	if shadow == nil {
		t.Fatal("no shadow at 1200,800")
	}
	if shadow.Peak > -0.7 || shadow.Peak < -1.3 {
		t.Errorf("shadow peak %.2f%%, want about -1%%", shadow.Peak)
	}
	if d := shadow.Diameter; d < 300 || d > 450 {
		t.Errorf("shadow diameter %d, want about 400", d)
	}
}

func TestCompareNewGoneMoved(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 9, 29, 4, 0, 0, 0, time.UTC)
	stay := disc{600, 500, 300, 0.01}
	a := flatOf(1, 1, t0, stay, disc{1800, 1100, 300, 0.008})
	b := flatOf(2, 2, t0.Add(24*time.Hour), stay, disc{1200, 800, 400, 0.01})
	got := changes(dustcheck.Compare(a, b))
	var newAt, goneAt bool
	for _, e := range got {
		switch {
		case e.Class == dustcheck.ClassNew && near(e, 1200, 800):
			newAt = true
		case e.Class == dustcheck.ClassGone && near(e, 1800, 1100):
			goneAt = true
		default:
			t.Errorf("unexpected %s at %d,%d", e.Class, e.X, e.Y)
		}
	}
	if !newAt || !goneAt {
		t.Errorf("changes %+v, want new at 1200,800 and gone at 1800,1100", got)
	}
	c := flatOf(3, 3, t0.Add(48*time.Hour), stay, disc{1330, 800, 400, 0.01})
	moved := changes(dustcheck.Compare(b, c))
	if len(moved) != 2 || moved[0].Class != dustcheck.ClassMoved || moved[1].Class != dustcheck.ClassMoved {
		t.Fatalf("changes %+v, want the two halves of a moved shadow", moved)
	}
	same := flatOf(4, 4, t0.Add(72*time.Hour), stay, disc{1330, 800, 400, 0.01})
	if got := changes(dustcheck.Compare(c, same)); len(got) != 0 {
		t.Errorf("unchanged flats gave %+v", got)
	}
}

func TestPreviousPrefersSameAngle(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const ha = "H-a"
	hist := []dustcheck.Flat{
		{ID: 1, Filter: ha, Rotator: 141, TakenAt: t0},
		{ID: 2, Filter: ha, Rotator: 52, TakenAt: t0.Add(time.Hour)},
		{ID: 3, Filter: "O-III", Rotator: 141, TakenAt: t0.Add(2 * time.Hour)},
	}
	cur := dustcheck.Flat{ID: 4, Filter: ha, Rotator: 142.2, TakenAt: t0.Add(3 * time.Hour)}
	if p, ok := dustcheck.Previous(cur, hist); !ok || p.ID != 1 {
		t.Errorf("previous %d, want 1 at the same angle", p.ID)
	}
	cur.Rotator = 90
	if p, ok := dustcheck.Previous(cur, hist); !ok || p.ID != 2 {
		t.Errorf("previous %d, want the latest, 2", p.ID)
	}
	cur.Filter = "S-II"
	if _, ok := dustcheck.Previous(cur, hist); ok {
		t.Error("found a previous flat for a filter with none")
	}
}

func located(filter string, x, y int, peak float64) dustcheck.Located {
	return dustcheck.Located{Filter: filter, Event: dustcheck.Event{Class: dustcheck.ClassNew,
		Blob: dustcheck.Blob{X: x, Y: y, Diameter: 500, Peak: peak}}}
}

func TestGroupChangesScope(t *testing.T) {
	t.Parallel()
	gs := dustcheck.GroupChanges([]dustcheck.Located{
		located(red, 1000, 1000, -1),
		located("Green", 1010, 995, -1),
		located("Blue", 3000, 2000, -0.4),
	}, 3, nil)
	if len(gs) != 2 || gs[0].Scope != dustcheck.ScopeWindow || gs[1].Scope != dustcheck.ScopeFilter {
		t.Fatalf("groups %+v, want a window change in two filters and a Blue filter change", gs)
	}
	if f := gs[0].Filters(); len(f) != 2 || f[0] != "Green" || f[1] != red {
		t.Errorf("filters %v", f)
	}
	others := map[string][]dustcheck.Blob{red: {{X: 1005, Y: 1000, Diameter: 520, Peak: -1}}}
	one := dustcheck.GroupChanges([]dustcheck.Located{located("Luminance", 1000, 1000, -1)}, 1, others)
	if one[0].Scope != dustcheck.ScopeWindow {
		t.Errorf("a new shadow other filters already hold is %s, want window", one[0].Scope)
	}
	lone := dustcheck.GroupChanges([]dustcheck.Located{located("Luminance", 3000, 3000, -1)}, 1, others)
	if lone[0].Scope != dustcheck.ScopeUnknown {
		t.Errorf("a lone change with one filter checked is %s, want unknown", lone[0].Scope)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	m := dustcheck.NewMap(7, 5)
	for i := range m.Pix {
		m.Pix[i] = float32(i)*0.01 - 0.1
	}
	b, err := dustcheck.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dustcheck.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.W != 7 || got.H != 5 || got.Pix[34] != m.Pix[34] {
		t.Errorf("round trip gave %dx%d %v", got.W, got.H, got.Pix[34])
	}
	if _, err := dustcheck.Decode(b[:10]); err == nil {
		t.Error("decoded a truncated map")
	}
	m.Pix[3] = float32(math.NaN())
	sb, err := dustcheck.EncodeSub(m)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := dustcheck.DecodeSub(sb)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(float64(sub.Pix[3])) || !math.IsNaN(float64(sub.Pix[0])) || math.Abs(float64(sub.Pix[30]-m.Pix[30])) > 1e-4 {
		t.Errorf("sub round trip gave %v %v %v", sub.Pix[3], sub.Pix[0], sub.Pix[30])
	}
	if _, err := dustcheck.DecodeSub(b); err == nil {
		t.Error("read a dust map as a sub map")
	}
}

func TestCropRendersThreePanels(t *testing.T) {
	t.Parallel()
	a := dustcheck.DustMap(synthFlat(1), synthW, synthH)
	b := dustcheck.DustMap(synthFlat(2, disc{1200, 800, 400, 0.01}), synthW, synthH)
	out, err := dustcheck.Crop(a, b, 1200, 800, 700)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := im.Bounds().Dx(), im.Bounds().Dy(); w != (3*350+12)*2 || h != 350*2 {
		t.Errorf("crop is %dx%d", w, h)
	}
	if _, err := dustcheck.Crop(a, b, 99999, 99999, 100); err == nil {
		t.Error("cropped outside the map")
	}
}

// synthLight is a calibrated light: flat sky with a gradient, stars, and
// the dust its flat missed.
func synthLight(seed, field uint64, discs ...disc) []float32 {
	rng := rand.New(rand.NewPCG(seed, field))
	pix := make([]float32, synthW*synthH)
	for y := range synthH {
		for x := range synthW {
			v := 0.1 * (1 + 0.05*float64(x)/synthW + 0.03*float64(y)/synthH)
			for _, d := range discs {
				if math.Hypot(float64(x-d.x), float64(y-d.y)) < float64(d.d)/2 {
					v *= 1 - d.depth
				}
			}
			pix[y*synthW+x] = float32(v * (1 + 0.02*rng.NormFloat64()))
		}
	}
	srng := rand.New(rand.NewPCG(field, 7))
	for range 400 {
		sx, sy := srng.IntN(synthW-10)+5, srng.IntN(synthH-10)+5
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				pix[(sy+dy)*synthW+sx+dx] += float32(0.5 * math.Exp(-float64(dx*dx+dy*dy)/3))
			}
		}
	}
	return pix
}

func TestResidualFindsDustTheFlatsMissed(t *testing.T) {
	t.Parallel()
	subs := make([]dustcheck.Map, 0, 40)
	for i := range uint64(40) {
		subs = append(subs, subMap(t, synthLight(i, i%4, disc{1200, 800, 500, 0.008}), synthW, synthH))
	}
	r, err := dustcheck.Residual(subs)
	if err != nil {
		t.Fatal(err)
	}
	discs := dustcheck.ResidualDiscs(dustcheck.Prepare(r))
	if len(discs) != 1 || !near(dustcheck.Event{Blob: discs[0]}, 1200, 800) || discs[0].Peak > -0.5 {
		t.Fatalf("residual discs %+v, want one shadow at 1200,800 about -0.8%%", discs)
	}
	clean := make([]dustcheck.Map, 0, 40)
	for i := range uint64(40) {
		clean = append(clean, subMap(t, synthLight(i, i%4), synthW, synthH))
	}
	r, err = dustcheck.Residual(clean)
	if err != nil {
		t.Fatal(err)
	}
	if d := dustcheck.ResidualDiscs(dustcheck.Prepare(r)); len(d) != 0 {
		t.Errorf("clean lights gave %+v", d)
	}
	if _, err := dustcheck.Residual(nil); err == nil {
		t.Error("no subs gave a residual")
	}
}

func TestLightsStatus(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		subs, fields int
		want         string
	}{{39, 3, dustcheck.LightsTooFew}, {40, 1, dustcheck.LightsOneField}, {40, 2, dustcheck.LightsChecked}} {
		if got := dustcheck.LightsStatus(c.subs, c.fields); got != c.want {
			t.Errorf("%d subs %d fields: %s, want %s", c.subs, c.fields, got, c.want)
		}
	}
}

func subMap(t *testing.T, pix []float32, w, h int) dustcheck.Map {
	t.Helper()
	m, err := dustcheck.SubMap(pix, w, h)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSubMapNeedsSky(t *testing.T) {
	t.Parallel()
	if _, err := dustcheck.SubMap(make([]float32, 64*64), 64, 64); err == nil {
		t.Error("a light with no sky gave a sub map")
	}
}

func TestCompareNeedsSameSize(t *testing.T) {
	t.Parallel()
	a := dustcheck.Flat{Map: dustcheck.Prepare(dustcheck.NewMap(200, 150))}
	b := dustcheck.Flat{Map: dustcheck.Prepare(dustcheck.NewMap(300, 150))}
	if evs := dustcheck.Compare(a, b); evs != nil {
		t.Errorf("maps of different sizes gave %+v", evs)
	}
	if _, err := dustcheck.Crop(a.Map.Map, b.Map.Map, 100, 100, 100); err == nil {
		t.Error("cropped maps of different sizes")
	}
}
