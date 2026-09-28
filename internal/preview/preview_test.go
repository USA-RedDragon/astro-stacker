package preview_test

import (
	"bytes"
	"image/jpeg"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
)

func TestMTF(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ m, x, want float64 }{
		{0.5, 0.3, 0.3}, // m = 0.5 is the identity
		{0.25, 0.25, 0.5},
		{0.1, 0, 0},
		{0.1, 1, 1},
	} {
		if got := preview.MTF(c.m, c.x); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("MTF(%v, %v) = %v, want %v", c.m, c.x, got, c.want)
		}
	}
}

// A sky-limited frame: background around 0.0092 (600 ADU of 65535) with
// noise, like a raw sub. The stretch should put the median at 0.25.
func TestStretchPutsMedianAtTargetBackground(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	p := make([]float32, 200_000)
	for i := range p {
		p[i] = float32(0.0092 + 0.0003*r.NormFloat64())
	}
	s := preview.Stretch(p)
	sorted := slices.Clone(s)
	slices.Sort(sorted)
	if med := sorted[len(sorted)/2]; math.Abs(float64(med)-preview.TargetBackground) > 0.01 {
		t.Errorf("stretched median = %v, want about %v", med, preview.TargetBackground)
	}
}

func TestRenderBinsAndEncodes(t *testing.T) {
	t.Parallel()
	im := &imagedata.Image{W: 4000, H: 300, C: 1, Data: make([]float32, 4000*300)}
	for i := range im.Data {
		im.Data[i] = float32(i%97) / 97
	}
	b, err := preview.Render(im, preview.Options{MaxWidth: 1600, Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// ceil(4000/1600) = 3, so 1333×100.
	if got := img.Bounds().Size(); got.X != 1333 || got.Y != 100 {
		t.Errorf("size %v", got)
	}
}
