package stacking

import (
	"bytes"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"testing"
)

// lineField is a synthetic field: sky with noise and a gradient, stars in
// every channel (H-a sees q of red's continuum), and an H II region whose
// line shows in red (lineInR of the H-a line) and in H-a only.
type lineField struct {
	w, h             int
	r, g, b, ha      []float32
	star             func(x, y int) bool
	nebulaX, nebulaY int
}

func newLineField(t *testing.T, reflection bool) lineField {
	t.Helper()
	const w, h, q, lineInR = 320, 320, 0.4, 0.2
	rng := rand.New(rand.NewPCG(1, 2))
	f := lineField{w: w, h: h, nebulaX: 220, nebulaY: 160}
	f.r, f.g, f.b, f.ha = make([]float32, w*h), make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)
	type st struct{ x, y, flux float64 }
	var stars []st
	for range 150 {
		stars = append(stars, st{8 + rng.Float64()*(w-16), 8 + rng.Float64()*(h-16), 0.005 + rng.Float64()*0.05})
	}
	f.star = func(x, y int) bool {
		for _, s := range stars {
			if math.Hypot(float64(x)-s.x, float64(y)-s.y) < 5 {
				return true
			}
		}
		return false
	}
	for y := range h {
		for x := range w {
			i := y*w + x
			var starlight float64
			for _, s := range stars {
				d2 := (float64(x)-s.x)*(float64(x)-s.x) + (float64(y)-s.y)*(float64(y)-s.y)
				if d2 < 36 {
					starlight += s.flux * math.Exp(-d2/(2*1.2*1.2))
				}
			}
			// The H II region: a smooth disc of line emission.
			dx, dy := float64(x-f.nebulaX), float64(y-f.nebulaY)
			line := 0.004 * math.Exp(-(dx*dx+dy*dy)/(2*35*35))
			// A reflection nebula on the left: blue continuum, and, when
			// asked for, the same line emission.
			rx, ry := float64(x-90), float64(y-160)
			refl := 0.0
			if reflection {
				refl = 0.006 * math.Exp(-(rx*rx+ry*ry)/(2*35*35))
				line += 0.002 * math.Exp(-(rx*rx+ry*ry)/(2*35*35))
			}
			sky := 0.01 + 0.001*float64(x)/w
			n := func() float64 { return rng.NormFloat64() * 5e-5 }
			f.r[i] = float32(sky + starlight + lineInR*line + 0.3*refl + n())
			f.g[i] = float32(sky + starlight + 0.5*refl + n())
			f.b[i] = float32(sky + starlight + refl + n())
			f.ha[i] = float32(0.005 + q*(starlight+0.3*refl) + line + n())
		}
	}
	return f
}

// The masked addition leaves the sky and the stars as they were,
// and adds red where H-a exceeds its continuum.
func TestAddLineMasked(t *testing.T) {
	t.Parallel()
	f := newLineField(t, false)
	out := addLine(lineInputs{Name: "H-a", Line: f.ha, Cont: f.r, Proxy: f.g, Gate: f.b,
		Into: []lineTarget{{Data: f.r}, {Data: f.b, MaxRel: haBlue}}}, f.w, f.h)
	red, blue := out[0], out[1]
	var changedOutside, starsChecked int
	for y := range f.h {
		for x := range f.w {
			i := y*f.w + x
			far := math.Hypot(float64(x-f.nebulaX), float64(y-f.nebulaY)) > 130
			if !far {
				continue
			}
			if f.star(x, y) {
				starsChecked++
			}
			// A tenth of the noise, or 0.1 % on a star: far below a JPEG
			// level either way.
			tol := max(5e-6, 1e-3*float64(f.r[i]))
			if math.Abs(float64(red[i]-f.r[i])) > tol || math.Abs(float64(blue[i]-f.b[i])) > tol {
				changedOutside++
			}
		}
	}
	if changedOutside > 0 {
		t.Errorf("%d pixels changed away from the nebula (%d of them on stars)", changedOutside, starsChecked)
	}
	var added, n float64
	for y := f.nebulaY - 15; y < f.nebulaY+15; y++ {
		for x := f.nebulaX - 15; x < f.nebulaX+15; x++ {
			i := y*f.w + x
			if !f.star(x, y) {
				added += float64(red[i] - f.r[i])
				n++
			}
		}
	}
	// The line in red at the centre is 0.2·0.004; doubled with lineGain.
	if want := 0.2 * 0.004 * 0.5; added/n < want {
		t.Errorf("red added in the nebula %.2e, want at least %.2e", added/n, want)
	}
	for i := range red {
		if red[i] < f.r[i] {
			t.Fatalf("pixel %d darkened", i)
		}
	}
}

// Line emission over bright blue continuum, as in a reflection nebula or
// the lavender centre of the North America Nebula, keeps its colour.
func TestAddLineGatesBlueContinuum(t *testing.T) {
	t.Parallel()
	f := newLineField(t, true)
	out := addLine(lineInputs{Name: "H-a", Line: f.ha, Cont: f.r, Proxy: f.g, Gate: f.b,
		Into: []lineTarget{{Data: f.r}}}, f.w, f.h)
	mean := func(cx int) float64 {
		var s, n float64
		for y := 150; y < 170; y++ {
			for x := cx - 10; x < cx+10; x++ {
				i := y*f.w + x
				if !f.star(x, y) {
					s += float64(out[0][i]-f.r[i]) / float64(f.r[i]-0.01)
					n++
				}
			}
		}
		return s / n
	}
	emission, reflection := mean(f.nebulaX), mean(90)
	if reflection > 0.2*emission {
		t.Errorf("red raised by %.2f in the reflection nebula against %.2f in the H II region", reflection, emission)
	}
}

// Without stars to measure the continuum on, nothing is added.
func TestAddLineWithoutStars(t *testing.T) {
	t.Parallel()
	const w, h = 64, 64
	flat := func(v float32) []float32 {
		p := make([]float32, w*h)
		for i := range p {
			p[i] = v + float32(i%5)*1e-5
		}
		return p
	}
	r := flat(0.01)
	out := addLine(lineInputs{Name: "H-a", Line: flat(0.02), Cont: r, Proxy: flat(0.01),
		Into: []lineTarget{{Data: r}}}, w, h)
	for i := range r {
		if out[0][i] != r[i] {
			t.Fatalf("pixel %d changed without a measurement", i)
		}
	}
}

// The cover's sky and stars are the same colour with and without H-a.
func TestCoverKeepsBackgroundColour(t *testing.T) {
	t.Parallel()
	f := newLineField(t, false)
	planes := map[string]*linearImage{
		"Red":   {W: f.w, H: f.h, Data: f.r},
		"Green": {W: f.w, H: f.h, Data: f.g},
		"Blue":  {W: f.w, H: f.h, Data: f.b},
		"H-a":   {W: f.w, H: f.h, Data: f.ha},
	}
	withHa, err := composeCover(palettes[0], planes, f.w, f.h)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := composeCover(palettes[1], planes, f.w, f.h)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := jpeg.Decode(bytes.NewReader(withHa))
	b, _ := jpeg.Decode(bytes.NewReader(plain))
	var diff, n float64
	for y := 10; y < 110; y++ {
		for x := 10; x < 110; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			diff += math.Abs(float64(r1>>8)-float64(r2>>8)) + math.Abs(float64(g1>>8)-float64(g2>>8)) + math.Abs(float64(b1>>8)-float64(b2>>8))
			n++
		}
	}
	if diff/n > 0.5 {
		t.Errorf("background differs by %.2f levels on average", diff/n)
	}
	r1, g1, _, _ := a.At(f.nebulaX, f.nebulaY).RGBA()
	r2, g2, _, _ := b.At(f.nebulaX, f.nebulaY).RGBA()
	if int(r1>>8)-int(g1>>8) <= int(r2>>8)-int(g2>>8) {
		t.Errorf("nebula no redder with H-a: %d,%d against %d,%d", r1>>8, g1>>8, r2>>8, g2>>8)
	}
}
