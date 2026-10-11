package stacking

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

type lrgbField struct {
	w, h    int
	r, g, b []float32
	base    []float64
}

func newLRGBField() lrgbField {
	const w, h = 320, 320
	rng := rand.New(rand.NewPCG(4, 4))
	f := lrgbField{w: w, h: h, r: make([]float32, w*h), g: make([]float32, w*h), b: make([]float32, w*h), base: make([]float64, w*h)}
	type st struct{ x, y, flux float64 }
	stars := make([]st, 0, 200)
	for range 200 {
		stars = append(stars, st{12 + rng.Float64()*(w-24), 12 + rng.Float64()*(h-24), 0.005 + rng.Float64()*0.05})
	}
	for y := range h {
		for x := range w {
			v := 0.01 + 0.001*float64(x)/w
			dx, dy := float64(x-200), float64(y-150)
			v += 0.003 * math.Exp(-(dx*dx+dy*dy)/(2*40*40))
			for _, s := range stars {
				if d2 := (float64(x)-s.x)*(float64(x)-s.x) + (float64(y)-s.y)*(float64(y)-s.y); d2 < 36 {
					v += s.flux * math.Exp(-d2/(2*1.2*1.2))
				}
			}
			i := y*w + x
			f.base[i] = v
			f.r[i] = float32(1.1*v + 2e-4*rng.NormFloat64())
			f.g[i] = float32(v + 2e-4*rng.NormFloat64())
			f.b[i] = float32(0.9*v + 2e-4*rng.NormFloat64())
		}
	}
	return f
}

// lum is a luminance of the field, shifted by dx pixels, with noise of
// sigma.
func (f lrgbField) lum(dx int, sigma float64) []float32 {
	rng := rand.New(rand.NewPCG(9, 9))
	out := make([]float32, f.w*f.h)
	for y := range f.h {
		for x := range f.w {
			sx := min(f.w-1, max(0, x-dx))
			out[y*f.w+x] = float32(0.004 + 2*f.base[y*f.w+sx] + sigma*rng.NormFloat64())
		}
	}
	return out
}

func (f lrgbField) stretch(l []float32) ([3][]float32, lrgbReport) {
	c := [3][]float32{f.r, f.g, f.b}
	return colourStretch(c, c, l, f.w, f.h)
}

// A deeper luminance goes into the lightness, weighted by inverse
// variance, and lowers its noise; the colour stays the channels' own.
func TestLRGBLightLowersNoiseAndKeepsColour(t *testing.T) {
	t.Parallel()
	f := newLRGBField()
	plain, none := f.stretch(nil)
	out, rep := f.stretch(f.lum(0, 1e-4))
	if none.Used || !rep.Used || rep.Stars < minSeamStars || rep.Shift > 0.1 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Weight < 0.5 || rep.Weight > 0.95 || rep.NoiseOut >= rep.NoiseRGB || rep.Gain() < 0.2 {
		t.Errorf("weight %.3f noise %.3g of %.3g gain %.2f", rep.Weight, rep.NoiseOut, rep.NoiseRGB, rep.Gain())
	}
	var n, off int
	for i := range out[0] {
		ok := true
		for c := range 3 {
			ok = ok && plain[c][i] > 0.05 && plain[c][i] < 0.95 && out[c][i] > 0.05 && out[c][i] < 0.95
		}
		if !ok {
			continue
		}
		n++
		for c := range 3 {
			if math.Abs(float64(out[c][i]/out[1][i]-plain[c][i]/plain[1][i])) > 1e-3 {
				off++
				break
			}
		}
	}
	if n < 1000 || off > 0 {
		t.Errorf("%d of %d pixels changed colour", off, n)
	}
}

// A luminance whose stars don't sit on the colour's, or that would barely
// lower the noise, is left out.
func TestLRGBLightLeavesOutWhatDoesNotHelp(t *testing.T) {
	t.Parallel()
	f := newLRGBField()
	plain, _ := f.stretch(nil)
	for _, c := range []struct {
		name  string
		l     []float32
		why   string
		stars bool
	}{
		{"shifted 2 px", f.lum(2, 1e-4), "stars sit", true},
		{"noisy", f.lum(0, 3e-3), "lower the noise by", false},
	} {
		out, rep := f.stretch(c.l)
		if rep.Used || !strings.Contains(rep.Why, c.why) {
			t.Errorf("%s: %+v", c.name, rep)
		}
		for k := range 3 {
			for i := range out[k] {
				if out[k][i] != plain[k][i] {
					t.Fatalf("%s: cover changed at %d", c.name, i)
				}
			}
		}
	}
}

func TestChoosePaletteOffersLuminance(t *testing.T) {
	t.Parallel()
	l := func(panels int) layer { return layer{Key: "k", Effective: 3600, Panels: panels} }
	for _, c := range []struct {
		name string
		have map[string]layer
		want string
		lum  bool
	}{
		{"RGB and L", map[string]layer{filterRed: l(0), filterGreen: l(0), filterBlue: l(0), filterLuminance: l(0)}, paletteRGB, true},
		{"RGB+Ha and L", map[string]layer{filterRed: l(0), filterGreen: l(0), filterBlue: l(0), filterHa: l(0), filterLuminance: l(0)}, paletteRGBHa, true},
		{"HOO and L", map[string]layer{filterHa: l(0), filterOIII: l(0), filterLuminance: l(0)}, paletteHOO, false},
		{"L on fewer panels", map[string]layer{filterRed: l(3), filterGreen: l(3), filterBlue: l(3), filterLuminance: l(2)}, paletteRGB, false},
		{"no L", map[string]layer{filterRed: l(0), filterGreen: l(0), filterBlue: l(0)}, paletteRGB, false},
	} {
		p, ok := choosePalette(c.have, false)
		if !ok || p.Name != c.want || p.Lum != c.lum {
			t.Errorf("%s: %s lum %v ok %v", c.name, p.Name, p.Lum, ok)
		}
	}
}

// A luminance that doesn't fit the colour's frame is left out, and one
// that does is used only inside its own well-covered crop.
func TestFramePlanesLuminance(t *testing.T) {
	t.Parallel()
	plane := func(w, h int) *linearImage {
		d := make([]float32, w*h)
		for i := range d {
			d[i] = 0.01
		}
		return &linearImage{W: w, H: h, Data: d}
	}
	pal := palettes()[1]
	pal.Lum = true
	linear := map[string]layer{filterRed: {}, filterGreen: {}, filterBlue: {}, filterLuminance: {CropX: 0.5, CropW: 0.5, CropH: 1}}
	p, planes, w, h, ok := framePlanes("t", pal, linear, map[string]*linearImage{
		filterRed: plane(100, 50), filterGreen: plane(100, 50), filterBlue: plane(100, 50), filterLuminance: plane(80, 50),
	})
	if !ok || p.Lum || planes[filterLuminance] != nil || w != 100 || h != 50 {
		t.Errorf("smaller luminance: lum %v plane %v %dx%d ok %v", p.Lum, planes[filterLuminance] != nil, w, h, ok)
	}
	p, planes, _, _, ok = framePlanes("t", pal, linear, map[string]*linearImage{
		filterRed: plane(100, 50), filterGreen: plane(100, 50), filterBlue: plane(100, 50), filterLuminance: plane(100, 50),
	})
	if !ok || !p.Lum {
		t.Fatalf("luminance left out")
	}
	if l := planes[filterLuminance].Data; l[10] != 0 || l[60] == 0 {
		t.Errorf("outside its crop %v, inside %v", l[10], l[60])
	}
}
