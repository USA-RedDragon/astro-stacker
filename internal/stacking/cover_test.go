package stacking

import (
	"bytes"
	"compress/gzip"
	"image/jpeg"
	"io"
	"io/fs"
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
	t.Parallel()
	hours := func(pairs ...any) map[string]layer {
		m := map[string]layer{}
		for i := 0; i+1 < len(pairs); i += 2 {
			name, okName := pairs[i].(string)
			hrs, okHours := pairs[i+1].(float64)
			if !okName || !okHours {
				t.Fatalf("hours(%v, %v): want a filter and its hours", pairs[i], pairs[i+1])
			}
			m[name] = layer{Key: "k", Effective: hrs * 3600}
		}
		return m
	}
	for _, c := range []struct {
		name string
		have map[string]layer
		want string
		sho  bool
	}{
		{"all RGB and H-a", hours(filterRed, 3.0, filterGreen, 3.0, filterBlue, 3.0, filterHa, 2.0), paletteRGBHa, false},
		// Crab Nebula: 2 H-a subs against 38 of each colour.
		{"too little H-a", hours(filterRed, 3.0, filterGreen, 3.0, filterBlue, 3.0, filterHa, 0.3), paletteRGB, false},
		{"one colour thin", hours(filterRed, 3.0, filterGreen, 3.0, filterBlue, 0.5, filterSII, 2.0, filterHa, 3.0, filterOIII, 2.5), paletteHOO, false},
		{"shot for SHO", hours(filterSII, 2.1, filterHa, 2.3, filterOIII, 1.9), paletteSHO, true},
		// California Nebula Panel 2: 1 S-II sub against 5 H-a.
		{"thin S-II", hours(filterSII, 0.1, filterHa, 0.8, filterOIII, 0.3), paletteHOO, false},
		{"H-a only", hours(filterHa, 5.0), "", false},
		// Dolphin Head: 7.7 h H-a and 5 h O-III against under an hour of
		// each colour.
		{"mostly narrowband", hours(filterRed, 0.5, filterGreen, 0.25, filterBlue, 0.4, filterHa, 7.7, filterOIII, 5.0), paletteHOO, false},
		// Cygnus Loop: S-II too, but SHO only when asked for.
		{"narrowband with S-II", hours(filterRed, 1.0, filterGreen, 1.0, filterBlue, 1.0, filterSII, 3.0, filterHa, 4.0, filterOIII, 3.0), paletteHOO, false},
		// IC 1396 Panel 2: O-III goes into green and blue too.
		{"RGB with H-a and O-III", hours(filterRed, 2.7, filterGreen, 2.9, filterBlue, 3.1, filterHa, 3.4, filterOIII, 3.4), "RGB+Ha+OIII", false},
		{"RGB with O-III only", hours(filterRed, 2.7, filterGreen, 2.9, filterBlue, 3.1, filterOIII, 3.4), "RGB+OIII", false},
		{"too little O-III", hours(filterRed, 2.7, filterGreen, 2.9, filterBlue, 3.1, filterHa, 3.4, filterOIII, 0.3), paletteRGBHa, false},
	} {
		p, ok := choosePalette(c.have, c.sho)
		if got := map[bool]string{true: p.Name, false: ""}[ok]; got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPaletteFilters(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		paletteRGBHa: {filterRed, filterGreen, filterBlue, filterHa},
		paletteRGB:   {filterRed, filterGreen, filterBlue},
		paletteSHO:   {filterSII, filterHa, filterOIII},
		paletteHOO:   {filterHa, filterOIII},
	}
	for _, p := range palettes() {
		if got := p.filters(); !slices.Equal(got, want[p.Name]) {
			t.Errorf("%s reads %v, want %v", p.Name, got, want[p.Name])
		}
	}
}

func TestLinearPreviewRoundTrip(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestCoverFromFiles renders a cover from downloaded linear previews named
// after their filters (Red.bin, H-a.bin, ...) in COVER_DIR, to cover.jpg.
func TestCoverFromFiles(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("COVER_DIR")
	if dir == "" {
		t.Skip("COVER_DIR not set")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	have := map[string]layer{}
	planes := map[string]*linearImage{}
	files, _ := fs.Glob(root.FS(), "*.bin")
	for _, f := range files {
		raw, err := root.ReadFile(f)
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
		name := strings.TrimSuffix(f, ".bin")
		// Assume equal data; this test is about how the cover looks.
		have[name], planes[name] = layer{Key: filepath.Join(dir, f), Effective: 1}, img
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
	if err := root.WriteFile("cover.jpg", jpg, 0o600); err != nil {
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
		filterRed:   plane(func(x int) float32 { return map[bool]float32{true: sky(x), false: 0}[x < w/2] }),
		filterGreen: plane(sky),
		filterBlue:  plane(sky),
	}
	jpg, err := composeCover(palettes()[1], planes, w, h) // RGB
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
		{"every filter current", []app.Stack{master(filterRed, &now, true), master(filterGreen, &now, true)}, true, false},
		{"restack under way", []app.Stack{master(filterRed, &now, true), master(filterGreen, &old, false)}, false, true},
		{"new filter without one yet", []app.Stack{master(filterRed, &now, true), {Filter: filterBlue, Subs: 20, MasterKey: &key}}, false, true},
		{"new filter too small for one", []app.Stack{master(filterRed, &now, true), {Filter: filterBlue, Subs: 2, MasterKey: &key}}, false, false},
		{"old one left, nothing coming", []app.Stack{master(filterRed, &now, true), master(filterGreen, &old, true)}, false, false},
		{"not a comet", []app.Stack{{Filter: filterRed, Subs: 20, MasterKey: &key}}, false, false},
	} {
		comet, wait := cometCover(c.stacks)
		if comet != c.comet || wait != c.wait {
			t.Errorf("%s: comet %v wait %v, want %v %v", c.name, comet, wait, c.comet, c.wait)
		}
	}
	// Comet masters from before the method was recorded are old ones.
	pre := master(filterGreen, &now, false)
	pre.CometMethod = nil
	if comet, wait := cometCover([]app.Stack{master(filterRed, &now, true), pre}); comet || !wait {
		t.Errorf("a comet master of unknown method: comet %v wait %v, want false true", comet, wait)
	}
}
