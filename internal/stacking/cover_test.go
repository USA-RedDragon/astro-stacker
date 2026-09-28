package stacking

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

func TestChoosePalette(t *testing.T) {
	hours := func(pairs ...any) map[string]layer {
		m := map[string]layer{}
		for i := 0; i < len(pairs); i += 2 {
			m[pairs[i].(string)] = layer{Key: "k", Effective: pairs[i+1].(float64) * 3600}
		}
		return m
	}
	for _, c := range []struct {
		name string
		have map[string]layer
		want string
	}{
		{"all RGB and H-a", hours("Red", 3.0, "Green", 3.0, "Blue", 3.0, "H-a", 2.0), "RGB+Ha"},
		// Crab Nebula: 2 H-a subs against 38 of each colour.
		{"too little H-a", hours("Red", 3.0, "Green", 3.0, "Blue", 3.0, "H-a", 0.3), "RGB"},
		{"one colour thin", hours("Red", 3.0, "Green", 3.0, "Blue", 0.5, "S-II", 2.0, "H-a", 3.0, "O-III", 2.5), "SHO"},
		// California Nebula Panel 2: 1 S-II sub against 5 H-a.
		{"thin S-II", hours("S-II", 0.1, "H-a", 0.8, "O-III", 0.3), "HOO"},
		{"H-a only", hours("H-a", 5.0), ""},
	} {
		p, ok := choosePalette(c.have)
		if got := map[bool]string{true: p.Name, false: ""}[ok]; got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLinearPreviewRoundTrip(t *testing.T) {
	im := &imagedata.Image{W: 4, H: 2, C: 1, Data: []float32{0, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7}}
	gz, err := linearPreview(im)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	got, err := decodeLinear(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.W != 4 || got.H != 2 || got.Data[7] != 0.7 {
		t.Fatalf("got %+v", got)
	}
}

func TestStretchKeepsUncoveredBlack(t *testing.T) {
	p := make([]float32, 1000)
	for i := 100; i < 1000; i++ {
		p[i] = 0.01 + float32(i%13)*0.0001
	}
	s := stretchNonZero(p)
	if s[0] != 0 {
		t.Errorf("uncovered pixel stretched to %v", s[0])
	}
	// The sky median lands near the target background.
	if v := s[500]; v < 0.1 || v > 0.5 {
		t.Errorf("sky pixel stretched to %v", v)
	}
}

func TestBlendHaOnlyBrightens(t *testing.T) {
	red := []float32{0.01, 0.011, 0.012, 0.01, 0.011, 0.012, 0.01, 0.011}
	ha := []float32{0.02, 0.021, 0.022, 0.02, 0.021, 0.022, 0.02, 0.2}
	out := blendHa(red, ha)
	if out[7] <= red[7] {
		t.Errorf("H-a nebula pixel not added to red: %v vs %v", out[7], red[7])
	}
	for i := range 7 {
		if out[i] < red[i]-1e-6 {
			t.Errorf("pixel %d darkened: %v < %v", i, out[i], red[i])
		}
	}
}

// TestCoverFromFiles renders a cover from downloaded linear previews named
// after their filters (Red.bin, H-a.bin, ...) in COVER_DIR, to cover.jpg.
func TestCoverFromFiles(t *testing.T) {
	dir := os.Getenv("COVER_DIR")
	if dir == "" {
		t.Skip("COVER_DIR not set")
	}
	have := map[string]layer{}
	planes := map[string]*linearImage{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.bin"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			raw, _ = io.ReadAll(zr)
		}
		img, err := decodeLinear(raw)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(f), ".bin")
		// Assume equal data; this test is about how the cover looks.
		have[name], planes[name] = layer{Key: f, Effective: 1}, img
	}
	pal, ok := choosePalette(have)
	if !ok {
		t.Fatal("no palette")
	}
	w, h := planes[pal.R].W, planes[pal.R].H
	jpg, err := composeCover(pal, planes, w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s, %dx%d", pal.Name, w, h)
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
}
