package stacking

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

func TestCometDesignation(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"C/2025 R2 (SWAN)":   "C/2025 R2",
		"C/2025 A6 (Lemmon)": "C/2025 A6",
		"12P/Pons-Brooks":    "12P",
		"Horsehead Nebula":   "",
		"M 13":               "",
	} {
		if got, _ := cometDesignation(name); got != want {
			t.Errorf("cometDesignation(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEphemeris(t *testing.T) {
	t.Parallel()
	e, err := parseEphemeris("header\n$$SOE\n 2025-Oct-20 02:50, , ,   283.85858,  -12.85657,\n 2025-Oct-20 02:51, , ,   283.86191,  -12.85595,\n$$EOE\n")
	if err != nil {
		t.Fatal(err)
	}
	ra, dec, ok := e.at(time.Date(2025, 10, 20, 2, 50, 30, 0, time.UTC))
	if !ok || math.Abs(ra-283.860245) > 1e-6 || math.Abs(dec+12.85626) > 1e-6 {
		t.Errorf("at 02:50:30 = %v %v %v", ra, dec, ok)
	}
	if _, _, ok := e.at(time.Date(2025, 10, 20, 2, 52, 0, 0, time.UTC)); ok {
		t.Error("a time past the table was interpolated")
	}
}

func TestShiftImage(t *testing.T) {
	t.Parallel()
	const w, h = 8, 6
	src := make([]float32, w*h)
	for i := range src {
		src[i] = float32(1 + i%w + 10*(i/w)) // value = 1 + x + 10y
	}
	out := shiftImage(src, w, h, 1.5, 2)
	// out(x, y) = src(x-1.5, y-2) = 1 + (x-1.5) + 10(y-2).
	if got, want := out[4*w+4], float32(1+2.5+20); math.Abs(float64(got-want)) > 1e-5 {
		t.Errorf("out(4,4) = %v, want %v", got, want)
	}
	if out[0] != 0 || out[1*w+7] != 0 {
		t.Error("pixels from outside the image are not empty")
	}
}

// Stacked with the comet's shifts, a comet moving against the stars comes
// out sharp and the stars, trailing through the shifted frames, are
// rejected.
func TestCometStack(t *testing.T) {
	t.Parallel()
	const w, h, n = 200, 150, 20
	r := rand.New(rand.NewPCG(7, 7))
	type star struct{ x, y, f float64 }
	var stars []star
	for range 40 {
		stars = append(stars, star{20 + r.Float64()*160, 20 + r.Float64()*110, 0.2})
	}
	blob := func(img []float32, cx, cy, f, s float64) {
		for y := int(cy) - 8; y <= int(cy)+8; y++ {
			for x := int(cx) - 8; x <= int(cx)+8; x++ {
				if x >= 0 && y >= 0 && x < w && y < h {
					dx, dy := float64(x)-cx, float64(y)-cy
					img[y*w+x] += float32(f * math.Exp(-(dx*dx+dy*dy)/(2*s*s)))
				}
			}
		}
	}
	subs := make([][]float32, n)
	shifts := make([][2]float64, n)
	for i := range subs {
		img := make([]float32, w*h)
		for j := range img {
			img[j] = float32(0.01 + 0.0005*r.NormFloat64())
		}
		for _, s := range stars {
			blob(img, s.x, s.y, s.f, 1.2)
		}
		cx, cy := 70+3*float64(i), 75.0 // 60 px of drift
		blob(img, cx, cy, 0.05, 2)
		subs[i] = img
		shifts[i] = [2]float64{70 - cx, 0}
	}
	stored := make([]storedSub, n)
	for i := range stored {
		stored[i] = storedSub{exposure: 10, weight: 10}
	}
	acc, err := streamStack(stored, 3, DefaultOptions, func(i int, _ storedSub) ([]float32, int, int, error) {
		return shiftImage(subs[i], w, h, shifts[i][0], shifts[i][1]), w, h, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := acc.Master(10)
	bg := slices.Clone(m)
	slices.Sort(bg)
	sky := bg[len(bg)/2]
	if peak := m[75*w+70] - sky; peak < 0.04 {
		t.Errorf("comet peak %.3f above sky, want about 0.05", peak)
	}
	// A star in the middle of the frame is spread over 60 px of trails and
	// mostly rejected: nothing near its original strength survives.
	for _, s := range stars {
		if s.x < 90 || s.x > 110 {
			continue
		}
		if v := m[int(s.y)*w+int(s.x)] - sky; v > 0.05 {
			t.Errorf("star at (%.0f,%.0f) left at %.3f", s.x, s.y, v)
		}
	}
}

// Lemmon's case: the comet moves little between subs, so each star lands on
// the same pixels in a few subs, which hold each other up against
// rejection. The median-anchored comet stack still removes them.
func TestCometStackShortDrift(t *testing.T) {
	t.Parallel()
	const w, h, n = 300, 120, 32
	r := rand.New(rand.NewPCG(8, 8))
	type star struct{ x, y float64 }
	var stars []star
	for range 80 {
		stars = append(stars, star{20 + r.Float64()*260, 20 + r.Float64()*80})
	}
	blob := func(img []float32, cx, cy, f, s float64) {
		for y := int(cy) - 8; y <= int(cy)+8; y++ {
			for x := int(cx) - 8; x <= int(cx)+8; x++ {
				if x >= 0 && y >= 0 && x < w && y < h {
					dx, dy := float64(x)-cx, float64(y)-cy
					img[y*w+x] += float32(f * math.Exp(-(dx*dx+dy*dy)/(2*s*s)))
				}
			}
		}
	}
	shifted := make([][]float32, n)
	for i := range shifted {
		img := make([]float32, w*h)
		for j := range img {
			img[j] = float32(0.01 + 0.0005*r.NormFloat64())
		}
		for _, s := range stars {
			blob(img, s.x, s.y, 0.2, 1.5)
		}
		drift := 3.4 * float64(i) // 105 px over the session, as for C/2025 A6
		blob(img, 60+drift, 60, 0.05, 3)
		shifted[i] = shiftImage(img, w, h, -drift, 0)
	}
	acc := cometAccumulator(shifted, w, h, 10, 10, DefaultOptions)
	m := acc.Master(10)
	bg := slices.Clone(m)
	slices.Sort(bg)
	sky := bg[len(bg)/2]
	worst := float32(0)
	for y := 5; y < h-5; y++ {
		for x := 5; x < w-110; x++ {
			if math.Hypot(float64(x-60), float64(y-60)) < 12 {
				continue // the comet
			}
			worst = max(worst, m[y*w+x]-sky)
		}
	}
	if worst > 0.01 {
		t.Errorf("a trailed star survives at %.3f above sky", worst)
	}
	if peak := m[60*w+60] - sky; peak < 0.04 {
		t.Errorf("comet peak %.3f, want about 0.05", peak)
	}
}
