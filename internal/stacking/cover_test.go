package stacking

import (
	"bytes"
	"compress/gzip"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
		sho  bool
	}{
		{"all RGB and H-a", hours("Red", 3.0, "Green", 3.0, "Blue", 3.0, "H-a", 2.0), "RGB+Ha", false},
		// Crab Nebula: 2 H-a subs against 38 of each colour.
		{"too little H-a", hours("Red", 3.0, "Green", 3.0, "Blue", 3.0, "H-a", 0.3), "RGB", false},
		{"one colour thin", hours("Red", 3.0, "Green", 3.0, "Blue", 0.5, "S-II", 2.0, "H-a", 3.0, "O-III", 2.5), "HOO", false},
		{"shot for SHO", hours("S-II", 2.1, "H-a", 2.3, "O-III", 1.9), "SHO", true},
		// California Nebula Panel 2: 1 S-II sub against 5 H-a.
		{"thin S-II", hours("S-II", 0.1, "H-a", 0.8, "O-III", 0.3), "HOO", false},
		{"H-a only", hours("H-a", 5.0), "", false},
		// Dolphin Head: 7.7 h H-a and 5 h O-III against under an hour of
		// each colour.
		{"mostly narrowband", hours("Red", 0.5, "Green", 0.25, "Blue", 0.4, "H-a", 7.7, "O-III", 5.0), "HOO", false},
		// Cygnus Loop: S-II too, but SHO only when asked for.
		{"narrowband with S-II", hours("Red", 1.0, "Green", 1.0, "Blue", 1.0, "S-II", 3.0, "H-a", 4.0, "O-III", 3.0), "HOO", false},
	} {
		p, ok := choosePalette(c.have, c.sho)
		if got := map[bool]string{true: p.Name, false: ""}[ok]; got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPaletteFilters(t *testing.T) {
	want := map[string][]string{
		"RGB+Ha": {"Red", "Green", "Blue", "H-a"},
		"RGB":    {"Red", "Green", "Blue"},
		"SHO":    {"S-II", "H-a", "O-III"},
		"HOO":    {"H-a", "O-III"},
	}
	for _, p := range palettes {
		if got := p.filters(); !slices.Equal(got, want[p.Name]) {
			t.Errorf("%s reads %v, want %v", p.Name, got, want[p.Name])
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
	pal, ok := choosePalette(have, false)
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

// A mosaic panel with green and blue but no red yet is left black, not cyan.
func TestCoverNeedsEveryChannel(t *testing.T) {
	t.Parallel()
	const w, h = 64, 32
	plane := func(fill func(x int) float32) *linearImage {
		d := make([]float32, w*h)
		for i := range d {
			d[i] = fill(i % w)
		}
		return &linearImage{W: w, H: h, Data: d}
	}
	sky := func(x int) float32 { return 0.01 + float32(x%7)*1e-4 }
	planes := map[string]*linearImage{
		"Red":   plane(func(x int) float32 { return map[bool]float32{true: sky(x), false: 0}[x < w/2] }),
		"Green": plane(sky),
		"Blue":  plane(sky),
	}
	jpg, err := composeCover(palettes[1], planes, w, h) // RGB
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(w*3/4, h/2).RGBA()
	if r>>8 > 8 || g>>8 > 8 || b>>8 > 8 {
		t.Errorf("pixel without red = %d,%d,%d, want black", r>>8, g>>8, b>>8)
	}
	if _, g, _, _ := img.At(w/4, h/2).RGBA(); g>>8 < 16 {
		t.Errorf("pixel with every channel is black")
	}
}

// A comet's cover uses its comet masters only when every filter's was made
// by the current method; while some are still being made, the cover is
// kept as it is.
func TestCometCover(t *testing.T) {
	t.Parallel()
	key, old, now := "k", cometMethod-1, cometMethod
	master := func(filter string, method *int, done bool) app.Stack {
		s := app.Stack{Filter: filter, Subs: 20, MasterKey: &key, UpdatedAt: time.Unix(1_700_000_000, 0)}
		if method != nil {
			s.CometLinearKey = &key
		}
		s.CometMethod = method
		if done {
			s.CometSignature = cometSignature(&s)
		}
		return s
	}
	for _, c := range []struct {
		name        string
		stacks      []app.Stack
		comet, wait bool
	}{
		{"every filter current", []app.Stack{master("Red", &now, true), master("Green", &now, true)}, true, false},
		{"restack under way", []app.Stack{master("Red", &now, true), master("Green", &old, false)}, false, true},
		{"new filter without one yet", []app.Stack{master("Red", &now, true), {Filter: "Blue", Subs: 20, MasterKey: &key}}, false, true},
		{"new filter too small for one", []app.Stack{master("Red", &now, true), {Filter: "Blue", Subs: 2, MasterKey: &key}}, false, false},
		{"old one left, nothing coming", []app.Stack{master("Red", &now, true), master("Green", &old, true)}, false, false},
		{"not a comet", []app.Stack{{Filter: "Red", Subs: 20, MasterKey: &key}}, false, false},
	} {
		comet, wait := cometCover(c.stacks)
		if comet != c.comet || wait != c.wait {
			t.Errorf("%s: comet %v wait %v, want %v %v", c.name, comet, wait, c.comet, c.wait)
		}
	}
	// Comet masters from before the method was recorded are old ones.
	pre := master("Green", &now, false)
	pre.CometMethod = nil
	if comet, wait := cometCover([]app.Stack{master("Red", &now, true), pre}); comet || !wait {
		t.Errorf("a comet master of unknown method: comet %v wait %v, want false true", comet, wait)
	}
}
