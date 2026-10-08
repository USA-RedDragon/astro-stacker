package publicframe

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// sky is a flat synthetic registered sub with Gaussian noise.
func sky(w, h int, seed uint64) *imagedata.Image {
	rng := rand.New(rand.NewPCG(seed, 1))
	im := &imagedata.Image{W: w, H: h, C: 1, Data: make([]float32, w*h)}
	for i := range im.Data {
		im.Data[i] = float32(0.1 + 0.004*rng.NormFloat64())
	}
	return im
}

// TestWatermarkOnSkyGrid checks the watermark is laid on the reference
// grid, not on the frame: the same sky shown through different crops
// carries it at the same sky position.
func TestWatermarkOnSkyGrid(t *testing.T) {
	t.Parallel()
	opts := Options{Width: 400, Height: 240, Quality: 60, Amplitude: 0.5}
	sub := sky(1600, 1000, 1)
	p := NewPattern(sub.W)
	a, err := Render(Input{Sub: sub, Crop: Rect{0, 0, 1200, 720}, Pattern: p}, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Same scale (3 reference pixels per output pixel), shifted 40, 20
	// output pixels.
	b, err := Render(Input{Sub: sub, Crop: Rect{120, 60, 1200, 720}, Pattern: p}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Region.W != b.Region.W || b.Region.X-a.Region.X != 120 || b.Region.Y-a.Region.Y != 60 {
		t.Fatalf("regions %+v, %+v", a.Region, b.Region)
	}
	var worst float64
	var letters int
	for v := range opts.Height - 20 {
		for u := range opts.Width - 40 {
			wa := float64(a.Weight[(v+20)*opts.Width+u+40])
			wb := float64(b.Weight[v*opts.Width+u])
			worst = max(worst, math.Abs(wa-wb))
			if wb > 0.9 {
				letters++
			}
		}
	}
	if worst > 1e-5 {
		t.Errorf("watermark moved with the crop: largest difference %g", worst)
	}
	if letters < 1000 {
		t.Errorf("only %d output pixels in letter cores", letters)
	}
}

// TestPatternScalesWithReference checks the letters are sized by the
// reference width, so they look the same whatever the camera's resolution.
func TestPatternScalesWithReference(t *testing.T) {
	t.Parallel()
	small, big := NewPattern(3000), NewPattern(6000)
	for _, xy := range [][2]float64{{10, 10}, {123.4, 56.7}, {1500, 900}, {2999, 42}} {
		if a, b := small.At(xy[0], xy[1]), big.At(2*xy[0], 2*xy[1]); math.Abs(a-b) > 1e-6 {
			t.Errorf("at %v: %g on a 3000 px reference, %g at twice the coordinates on 6000 px", xy, a, b)
		}
	}
}

// TestRenderedFrame checks the frame's size and that it carries no
// metadata: no EXIF (APP1) or comment segments.
func TestRenderedFrame(t *testing.T) {
	t.Parallel()
	res, err := Render(Input{Sub: sky(2000, 1300, 2), Pattern: NewPattern(2000)}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(res.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 480 {
		t.Errorf("frame is %dx%d", b.Dx(), b.Dy())
	}
	for _, marker := range [][]byte{{0xff, 0xe1}, {0xff, 0xfe}, []byte("Exif")} {
		if bytes.Contains(res.JPEG, marker) {
			t.Errorf("frame contains metadata marker % x", marker)
		}
	}
	if res.Sigma <= 0 || math.Abs(res.Amplitude-DefaultOptions().Amplitude*res.Sigma) > 1e-9 {
		t.Errorf("sigma %g, amplitude %g", res.Sigma, res.Amplitude)
	}
}

// TestWatermarkSurvivesJPEG checks the sub-noise watermark comes through
// quantizing and JPEG at quality 60 on average over frames: the sky noise
// dithers it.
func TestWatermarkSurvivesJPEG(t *testing.T) {
	t.Parallel()
	opts := DefaultOptions()
	const frames = 16
	sum := make([]float64, opts.Width*opts.Height)
	var weight []float32
	var amp float64
	for i := range frames {
		sub := sky(1600, 960, uint64(10+i))
		in := Input{Sub: sub, Pattern: NewPattern(sub.W)}
		wm, err := Render(in, opts)
		if err != nil {
			t.Fatal(err)
		}
		ctl := opts
		ctl.Amplitude = 0
		c, err := Render(in, ctl)
		if err != nil {
			t.Fatal(err)
		}
		a, b := decode(t, wm.JPEG), decode(t, c.JPEG)
		for j := range sum {
			sum[j] += float64(a[j]) - float64(b[j])
		}
		weight, amp = wm.Weight, amp+wm.Amplitude/frames
	}
	var core, coreN, off, offN float64
	for j, w := range weight {
		switch {
		case w > 0.9:
			core += sum[j] / frames
			coreN++
		case w == 0:
			off += sum[j] / frames
			offN++
		}
	}
	core, off = core/coreN, off/offN
	if core < 0.75*amp || core > 1.25*amp {
		t.Errorf("letter cores average %.2f DN brighter, want about the %.2f DN added", core, amp)
	}
	if math.Abs(off) > 0.1*amp {
		t.Errorf("sky outside the letters moved %.2f DN", off)
	}
}

func decode(t *testing.T, b []byte) []uint8 {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("frame decodes as %T, not grey", img)
	}
	return g.Pix
}

// field is a synthetic registered sub w×h with Gaussian noise of sd over
// sky (0.1) plus extra(x, y), and its master: the same without noise,
// binned 4×.
func field(w, h int, sd float64, seed uint64, extra func(x, y int) float64) (*imagedata.Image, *Master) {
	rng := rand.New(rand.NewPCG(seed, 1))
	im := &imagedata.Image{W: w, H: h, C: 1, Data: make([]float32, w*h)}
	const bin = 4
	mw, mh := w/bin, h/bin
	m := &Master{W: mw, H: mh, Scale: bin, Plane: make([]float32, mw*mh)}
	for y := range h {
		for x := range w {
			v := 0.1 + extra(x, y)
			im.Data[y*w+x] = float32(v + sd*rng.NormFloat64())
			if x/bin < mw && y/bin < mh {
				m.Plane[(y/bin)*mw+x/bin] += float32(v / (bin * bin))
			}
		}
	}
	return im, m
}

// nebula is smooth structure at the letters' scale.
func nebula(amp float64) func(x, y int) float64 {
	return func(x, y int) float64 {
		return amp * math.Sin(float64(x)/37) * math.Sin(float64(y)/29)
	}
}

// stars is a dense field of point sources and nothing extended.
func stars(x, y int) float64 {
	if (x*7919+y*104729)%997 == 0 {
		return 0.05
	}
	return 0
}

// TestStructureNoise checks noise is added only where the master's
// structure would hide the watermark from a stack: a smooth sub of a
// nebulous field gets enough to bring its noise to StructureRatio × the
// structure; a noise-limited sub, or one whose only structure is stars,
// is rendered exactly as without the rule.
func TestStructureNoise(t *testing.T) {
	t.Parallel()
	opts := DefaultOptions()
	off := opts
	off.StructureRatio = 0
	render := func(sub *imagedata.Image, m *Master, o Options) Result {
		t.Helper()
		res, err := Render(Input{Sub: sub, Pattern: NewPattern(sub.W), Master: m}, o)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	sub, m := field(1600, 960, 0.0005, 1, nebula(0.002))
	opts.Seed = 5
	res := render(sub, m, opts)
	if res.Added == 0 {
		t.Fatalf("nebulous field: no noise added (noise %g, clutter %g)", res.NoiseLinear, res.Clutter)
	}
	if got := math.Hypot(res.NoiseLinear, res.Added); math.Abs(got-opts.StructureRatio*res.Clutter) > 1e-9 {
		t.Errorf("noise brought to %g, want %g × clutter %g", got, opts.StructureRatio, res.Clutter)
	}

	for name, f := range map[string]func(x, y int) float64{
		"noise-limited": nebula(0.00002),
		"stars only":    stars,
	} {
		sub, m := field(1600, 960, 0.004, 2, f)
		with, without := render(sub, m, opts), render(sub, nil, off)
		if with.Added != 0 {
			t.Errorf("%s: noise added (noise %g, clutter %g)", name, with.NoiseLinear, with.Clutter)
		}
		if !bytes.Equal(with.JPEG, without.JPEG) {
			t.Errorf("%s: frame differs from one rendered without the rule", name)
		}
	}
}

// TestNoiseFresh checks the added noise differs every render unless seeded.
func TestNoiseFresh(t *testing.T) {
	t.Parallel()
	sub, m := field(1600, 960, 0.0005, 3, nebula(0.002))
	in := Input{Sub: sub, Pattern: NewPattern(sub.W), Master: m}
	a, _ := Render(in, DefaultOptions())
	b, _ := Render(in, DefaultOptions())
	if a.Added == 0 || bytes.Equal(a.JPEG, b.JPEG) {
		t.Error("two unseeded renders added the same noise")
	}
	o := DefaultOptions()
	o.Seed = 9
	c, _ := Render(in, o)
	d, _ := Render(in, o)
	if !bytes.Equal(c.JPEG, d.JPEG) {
		t.Error("seeded renders differ")
	}
}
