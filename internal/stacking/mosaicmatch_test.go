package stacking

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// Siril's registered panels, as -framing=max writes them: each cropped to
// its own part of a canvas, on one projection, the reference pixel moved.
const (
	canvasW, canvasH = 480, 360
	refCol, refRow   = 240.5, 180.5 // the projection's reference, canvas FITS pixels
)

type registeredPanel struct {
	X, Y, W, H int // top-left on the canvas, top row first
}

func (s registeredPanel) cards() []imagedata.Card {
	return []imagedata.Card{
		imagedata.FloatCard("CRVAL1", 314.13, ""), imagedata.FloatCard("CRVAL2", 43.98, ""),
		imagedata.FloatCard("CRPIX1", refCol-float64(s.X), ""),
		imagedata.FloatCard("CRPIX2", refRow-float64(canvasH-s.Y-s.H), ""),
		imagedata.FloatCard("CDELT1", -5.3e-4, ""), imagedata.FloatCard("CDELT2", 5.3e-4, ""),
		imagedata.FloatCard("PC1_1", 1, ""), imagedata.FloatCard("PC1_2", 0, ""),
		imagedata.FloatCard("PC2_1", 0, ""), imagedata.FloatCard("PC2_2", 1, ""),
	}
}

func TestPlacementsFromRegisteredPanels(t *testing.T) {
	t.Parallel()
	panels := []registeredPanel{{0, 0, 478, 220}, {2, 140, 478, 220}}
	headers := make([]frameheader.Keywords, 0, len(panels))
	sizes := make([][2]int, 0, len(panels))
	for _, p := range panels {
		kw := frameheader.Keywords{}
		for _, c := range p.cards() {
			kw[c.Key] = c.Value
		}
		headers = append(headers, kw)
		sizes = append(sizes, [2]int{p.W, p.H})
	}
	at, err := placements(headers, sizes)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range panels {
		if want := (placement{p.X, p.Y, canvasW, canvasH}); at[i] != want {
			t.Errorf("panel %d at %+v, want %+v", i+1, at[i], want)
		}
	}
	// Panels on different projections can't be placed.
	headers[1]["CRVAL1"] = "314.2"
	if _, err := placements(headers, sizes); err == nil {
		t.Error("panels on different projections were placed")
	}
}

// Two panels, the second brighter and with a gradient across it the first
// doesn't have, are matched on their overlap: the difference is split
// between them and gone where they overlap, whatever the stars and
// nebulosity they share.
func TestMatchRegisteredPanels(t *testing.T) {
	t.Parallel()
	const sat = 0.9
	rng := rand.New(rand.NewPCG(1, 2))
	scene := make([]float64, canvasW*canvasH)
	for r := range canvasH {
		for c := range canvasW {
			v := 0.01 + 0.002*float64(r)/canvasH
			// nebulosity across the overlap
			v += 0.02 * math.Exp(-(math.Pow(float64(c)-300, 2)+math.Pow(float64(r)-190, 2))/(2*40*40))
			scene[r*canvasW+c] = v
		}
	}
	for range 400 {
		scene[rng.IntN(len(scene))] += 0.3
	}
	offset := func(c int) float64 { return 0.002 + 0.001*((float64(c)+0.5)/canvasW-0.5) }
	panels := []registeredPanel{{0, 0, 478, 220}, {2, 140, 478, 220}}
	dir := t.TempDir()
	files := make([]string, 0, len(panels))
	for k, p := range panels {
		data := make([]float32, p.W*p.H)
		for y := range p.H {
			for x := range p.W {
				c, r := x+p.X, y+p.Y
				v := scene[r*canvasW+c] + 1e-4*rng.NormFloat64()
				if k == 1 {
					v += offset(c)
				}
				data[y*p.W+x] = float32(v)
			}
		}
		data[5*p.W+5] = 0.95 // saturated, left alone
		f := filepath.Join(dir, fmt.Sprintf("r_pan_%05d.fit", k+1))
		if err := writeFITSFile(f, p.W, p.H, 1, data, append(panels[k].cards(), imagedata.StringCard("OBJECT", "panel", ""))); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	before, after, err := matchRegistered(files, sat)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("pairs %+v, %+v", before, after)
	}
	// Panel 1 less panel 2, at the overlap's centre (canvas x ≈ 0).
	if m := before[0].Fit.Mean; math.Abs(m+offset(canvasW/2)) > 1e-4 {
		t.Errorf("measured difference %v, want %v", m, -offset(canvasW/2))
	}
	if bx := before[0].Fit.Plane.Bx; math.Abs(bx+0.001) > 1e-4 {
		t.Errorf("measured slope %v, want -0.001", bx)
	}
	if m := after[0].Fit.Mean; math.Abs(m) > 1e-5 {
		t.Errorf("difference after matching %v", m)
	}

	read := func(f string) *imagedata.Image {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		kw, err := frameheader.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		if kw["OBJECT"] != "panel" || kw["CRPIX2"] == "" {
			t.Errorf("%s lost its header", filepath.Base(f))
		}
		return im
	}
	p1, p2 := read(files[0]), read(files[1])
	var diffs []float64
	for r := 145; r < 215; r++ {
		for c := 5; c < 475; c++ {
			a := p1.Data[(r-panels[0].Y)*p1.W+c-panels[0].X]
			b := p2.Data[(r-panels[1].Y)*p2.W+c-panels[1].X]
			diffs = append(diffs, float64(a-b))
		}
	}
	if m := median(slices.Clone(diffs)); math.Abs(m) > 2e-5 {
		t.Errorf("overlap differs by %v after matching", m)
	}
	// The difference is split: panel 1, away from the overlap, goes up
	// by half of it.
	c, r := 100, 20
	got := float64(p1.Data[(r-panels[0].Y)*p1.W+c-panels[0].X]) - scene[r*canvasW+c]
	if want := offset(c) / 2; math.Abs(got-want) > 5e-4 {
		t.Errorf("panel 1 moved by %v, want %v", got, want)
	}
	if p1.Data[5*p1.W+5] != 0.95 || p2.Data[5*p2.W+5] != 0.95 {
		t.Error("saturated pixels changed")
	}
}

// Corrections for a chain of panels satisfy every overlap and keep the
// panels' mean level.
func TestPanelCorrections(t *testing.T) {
	t.Parallel()
	pairs := []panelPair{
		{I: 0, J: 1, Fit: overlapFit{Plane: plane{A: 0.003, Bx: 0.001}, Samples: 1000}},
		{I: 1, J: 2, Fit: overlapFit{Plane: plane{A: -0.001, By: 0.002}, Samples: 500}},
	}
	corr := panelCorrections(3, pairs)
	for _, p := range pairs {
		d := corr[p.J]
		d.A, d.Bx, d.By = d.A-corr[p.I].A, d.Bx-corr[p.I].Bx, d.By-corr[p.I].By
		if math.Abs(d.A-p.Fit.Plane.A) > 1e-9 || math.Abs(d.Bx-p.Fit.Plane.Bx) > 1e-9 || math.Abs(d.By-p.Fit.Plane.By) > 1e-9 {
			t.Errorf("panels %d-%d: corrections differ by %+v, want %+v", p.I, p.J, d, p.Fit.Plane)
		}
	}
	var sum plane
	for _, c := range corr {
		sum.A, sum.Bx, sum.By = sum.A+c.A, sum.Bx+c.Bx, sum.By+c.By
	}
	if math.Abs(sum.A)+math.Abs(sum.Bx)+math.Abs(sum.By) > 1e-9 {
		t.Errorf("corrections sum to %+v", sum)
	}
}

// The downloadable mosaic is a FITS marked bottom-up and an XISF copy
// with the same pixels, upright, and the plate solution as properties.
func TestPublishableMosaic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const w, h = 6, 4
	data := make([]float32, w*h)
	for i := range data {
		data[i] = float32(i+1) / 100
	}
	in := filepath.Join(dir, "mosaic.fit")
	cards := append(registeredPanel{0, 0, w, h}.cards(), imagedata.StringCard("FILTER", filterLuminance, ""))
	if err := writeFITSFile(in, w, h, 1, data, cards); err != nil {
		t.Fatal(err)
	}
	fitsOut, xisfOut := filepath.Join(dir, "d.fit"), filepath.Join(dir, "d.xisf")
	if err := publishableMosaic(in, fitsOut, xisfOut); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{fitsOut, xisfOut} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(im.Data, data) {
			t.Errorf("%s: pixels %v, want %v", filepath.Base(f), im.Data, data)
		}
		kw, err := frameheader.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		if kw["FILTER"] != filterLuminance {
			t.Errorf("%s: keywords %v", filepath.Base(f), kw)
		}
		if f == fitsOut && kw["ROWORDER"] != "BOTTOM-UP" {
			t.Errorf("FITS ROWORDER %q", kw["ROWORDER"])
		}
		if f == xisfOut {
			if !bytes.Contains(b, []byte("PCL:AstrometricSolution:ReferenceImageCoordinates")) {
				t.Error("XISF has no plate solution")
			}
			if bytes.Contains(b, []byte("ROWORDER")) {
				t.Error("XISF carries a ROWORDER")
			}
		}
	}
}
