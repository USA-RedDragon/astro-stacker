package calpreview

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/preview"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
)

// BenchmarkMasterLoad is the one-time cost of a master the stacker already
// has in its work directory: reading a full-size float FITS and binning it.
func BenchmarkMasterLoad(b *testing.B) {
	const w, h = 6248, 4176
	dir := b.TempDir()
	data := make([]float32, w*h)
	for i := range data {
		data[i] = 0.01
	}
	if err := os.MkdirAll(filepath.Join(dir, "masters"), 0o700); err != nil {
		b.Fatal(err)
	}
	f, err := os.Create(stacking.BuiltMasterFile(dir, "k"))
	if err != nil {
		b.Fatal(err)
	}
	if err := imagedata.WriteFITS(f, w, h, 1, data, nil); err != nil {
		b.Fatal(err)
	}
	_ = f.Close()
	c := &Calibrator{WorkDir: dir}
	ch := chosen{match: calmatch.Match{Set: &calmatch.Set{Type: "BIAS"}}, key: builtKey{key: "k"}}
	for b.Loop() {
		if _, err := c.read(context.Background(), ch, nil, 5); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDarkScaleFindsKnownScale(t *testing.T) {
	t.Parallel()
	const w, h = 400, 300
	r := rand.New(rand.NewPCG(1, 2))
	dark := make([]float32, w*h)
	light := make([]float32, w*h)
	for i := range dark {
		dark[i] = float32(0.002 + 0.001*r.NormFloat64())
		if r.IntN(50) == 0 {
			dark[i] += 0.2
		}
		light[i] = 0.05 + 0.4*dark[i] + float32(0.0005*r.NormFloat64())
	}
	k := DarkScale(light, dark, w, h)
	if math.Abs(k-0.4) > 0.01 {
		t.Fatalf("scale %.4f, want 0.4", k)
	}
}

func TestDivideFlatEvensVignetting(t *testing.T) {
	t.Parallel()
	flat := []float32{0.5, 1, 1, 0.5}
	data := []float32{0.25, 0.5, 0.5, 0.25}
	divideFlat(data, flat)
	for i, v := range data {
		if math.Abs(float64(v-data[0])) > 1e-6 {
			t.Fatalf("pixel %d is %v, want all equal: %v", i, v, data)
		}
	}
}

func TestCaptionNamesMastersAndFallbacks(t *testing.T) {
	t.Parallel()
	night := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	dark := chosen{match: calmatch.Match{Set: &calmatch.Set{Type: "DARK", SetTemp: -20, Exposure: 300}, Scaled: true}}
	flat := chosen{match: calmatch.Match{Set: &calmatch.Set{Type: "FLAT", Night: night.AddDate(0, 0, -5)}},
		skipped: &calmatch.Set{Type: "FLAT", Night: night}}
	bias := chosen{match: calmatch.Match{Set: &calmatch.Set{Type: "BIAS", Night: night.AddDate(0, 0, -16)}}}
	got := caption(bias, dark, flat)
	want := "Calibrated with the -20 C 300 s dark (scaled to fit), Oct 12 flats and Oct 1 bias; the Oct 17 flats are not stacked yet"
	if got != want {
		t.Fatalf("caption\n got %q\nwant %q", got, want)
	}
	got = caption(bias, chosen{}, flat)
	if !strings.Contains(got, "no matching dark master yet") {
		t.Fatalf("caption %q does not say the dark is missing", got)
	}
}

// BenchmarkPerFrame is the work added to each light once its masters are in
// memory: binning a full IMX571 frame, calibrating it, and encoding the JPEG.
func BenchmarkPerFrame(b *testing.B) {
	const w, h = 6248, 4176
	r := rand.New(rand.NewPCG(3, 4))
	full := &imagedata.Image{W: w, H: h, C: 1, Data: make([]float32, w*h)}
	for i := range full.Data {
		full.Data[i] = float32(0.02 + 0.002*r.NormFloat64())
	}
	opts := preview.Options{MaxWidth: 1280, Quality: 80}
	factor := int(math.Ceil(float64(w) / float64(opts.MaxWidth)))
	m := preview.Bin(full, factor)
	bias, dark, flat := cloneFill(m, 0.01), cloneFill(m, 0.001), cloneFill(m, 0.5)
	b.ResetTimer()
	for b.Loop() {
		light := preview.Bin(full, factor)
		for i := range light.Data {
			light.Data[i] -= bias.Data[i]
		}
		k := DarkScale(light.Data, dark.Data, light.W, light.H)
		for i := range light.Data {
			light.Data[i] -= float32(k) * dark.Data[i]
		}
		divideFlat(light.Data, flat.Data)
		if _, err := preview.Render(light, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func cloneFill(m *imagedata.Image, v float32) *imagedata.Image {
	out := &imagedata.Image{W: m.W, H: m.H, C: 1, Data: make([]float32, len(m.Data))}
	for i := range out.Data {
		out.Data[i] = v * (1 + 0.01*float32(i%7))
	}
	return out
}
