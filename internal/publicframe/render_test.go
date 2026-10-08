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
	res, err := Render(Input{Sub: sky(2000, 1300, 2), Pattern: NewPattern(2000)}, DefaultOptions)
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
	if res.Sigma <= 0 || math.Abs(res.Amplitude-DefaultOptions.Amplitude*res.Sigma) > 1e-9 {
		t.Errorf("sigma %g, amplitude %g", res.Sigma, res.Amplitude)
	}
}

// TestWatermarkSurvivesJPEG checks the sub-noise watermark comes through
// quantizing and JPEG at quality 60 on average over frames: the sky noise
// dithers it.
func TestWatermarkSurvivesJPEG(t *testing.T) {
	opts := DefaultOptions
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

// TestNoiseFloor checks noise is added only to frames smoother than the
// floor, and the watermark scales with the noise shown.
func TestNoiseFloor(t *testing.T) {
	sub := sky(1600, 960, 3)
	in := Input{Sub: sub, Pattern: NewPattern(sub.W)}
	plain, err := Render(in, DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions
	opts.NoiseFloor, opts.Seed = 3*plain.Sigma, 7
	floored, err := Render(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if floored.Sigma != opts.NoiseFloor || floored.Amplitude != opts.Amplitude*opts.NoiseFloor {
		t.Errorf("floored sigma %g, amplitude %g; floor %g", floored.Sigma, floored.Amplitude, opts.NoiseFloor)
	}
	opts.NoiseFloor = plain.Sigma / 2
	low, err := Render(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(low.JPEG, plain.JPEG) {
		t.Error("a floor below the frame's noise changed it")
	}
}
