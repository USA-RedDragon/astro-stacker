package stacking

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func writeStarPanels(t *testing.T, shift, ratio float64) []string {
	t.Helper()
	rng := rand.New(rand.NewPCG(5, 6))
	panels := []registeredPanel{{0, 0, 478, 220}, {2, 140, 478, 220}}
	type star struct{ x, y float64 }
	var stars []star
	for _, r := range []float64{160.3, 185.6} {
		for c := 30.2; c < 450; c += 24 {
			stars = append(stars, star{c, r})
		}
	}
	dir := t.TempDir()
	files := make([]string, 0, len(panels))
	for k, p := range panels {
		data := make([]float32, p.W*p.H)
		for y := range p.H {
			for x := range p.W {
				c, r := float64(x+p.X)+0.5, float64(y+p.Y)+0.5
				v := 0.01 + 1e-4*rng.NormFloat64()
				for _, s := range stars {
					sx, amp := s.x, 0.05
					if k == 1 {
						sx += shift
						amp /= ratio
					}
					dx, dy := c-sx, r-s.y
					if dx*dx+dy*dy < 100 {
						v += amp * math.Exp(-(dx*dx+dy*dy)/(2*1.6*1.6))
					}
				}
				data[y*p.W+x] = float32(v)
			}
		}
		f := filepath.Join(dir, fmt.Sprintf("r_pan_%05d.fit", k+1))
		if err := writeFITSFile(f, p.W, p.H, 1, data, panels[k].cards()); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

func TestSeamStarsMeasureRegistrationAndFlux(t *testing.T) {
	t.Parallel()
	files := writeStarPanels(t, 0.8, 1.25)
	_, _, rep, err := matchRegisteredSeams(files, 0.9, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.seams) != 1 {
		t.Fatalf("seams %+v", rep.seams)
	}
	m := rep.seams[0]
	if m.StarMatches < 30 {
		t.Fatalf("%d stars matched", m.StarMatches)
	}
	if math.Abs(m.RegMedian-0.8) > 0.15 || m.RegP90 < m.RegMedian || m.RegP90 > 1.1 {
		t.Errorf("registration median %v p90 %v, want about 0.8", m.RegMedian, m.RegP90)
	}
	if math.Abs(m.FluxRatio-1.25) > 0.04 {
		t.Errorf("flux ratio %v, want 1.25", m.FluxRatio)
	}
	if s := rep.noise.Scales; len(s) != 2 || math.Abs(s[0]/s[1]-1.25) > 0.04 || math.Abs(s[0]*s[1]-1) > 1e-6 {
		t.Errorf("panel scales %v", s)
	}
	if rep.noise.Tiles == 0 || !(rep.noise.P90 > 0) || rep.noise.Max < rep.noise.P90 || rep.noise.P90 < rep.noise.Median {
		t.Errorf("noise %+v", rep.noise)
	}
	rec := seamRecord(rhoProject, "p", "R", seamPanel{Number: 1}, seamPanel{Number: 2}, m)
	if rec.StarMatches != m.StarMatches || rec.RegP90 == nil || rec.FluxRatio == nil || math.Abs(*rec.FluxRatio-1.25) > 0.04 {
		t.Errorf("record %+v", rec)
	}
	if strings.Contains(rec.Problems, "misregistered") {
		t.Errorf("sub-pixel offset flagged: %s", rec.Problems)
	}
}

func TestSeamStarsFlagMisregistration(t *testing.T) {
	t.Parallel()
	files := writeStarPanels(t, 2.2, 1)
	_, _, rep, err := matchRegisteredSeams(files, 0.9, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.seams) != 1 || rep.seams[0].StarMatches < 30 {
		t.Fatalf("seams %+v", rep.seams)
	}
	rec := seamRecord(rhoProject, "p", "R", seamPanel{Number: 1}, seamPanel{Number: 2}, rep.seams[0])
	if rec.OK || !strings.Contains(rec.Problems, "stars misregistered by 2.") {
		t.Errorf("record %+v", rec)
	}
}

func TestSeamRecordWithoutStarsLeavesMetricsNull(t *testing.T) {
	t.Parallel()
	m := seamMeasure{Fit: overlapFit{Samples: 1000}, NoiseA: 1, NoiseB: 1, StarMatches: 3, RegMedian: 0.2, RegP90: 0.3, FluxRatio: 1}
	r := seamRecord("M", "g", "R", seamPanel{Number: 1}, seamPanel{Number: 2}, m)
	if r.RegMedian != nil || r.RegP90 != nil || r.FluxRatio != nil || r.StarMatches != 3 {
		t.Errorf("too few stars still reported: %+v", r)
	}
}

func uniformPanel(w, h int, noise float32) binnedPanel {
	b := binnedPanel{W: w, H: h, fullW: w * matchBin, fullH: h * matchBin, Data: make([]float32, w*h), Noise: make([]float32, w*h)}
	for i := range b.Noise {
		b.Noise[i] = noise
	}
	return b
}

func TestMosaicNoiseNormalisesPanelsToTheMosaicScale(t *testing.T) {
	t.Parallel()
	a, b := uniformPanel(32, 16, 1), uniformPanel(32, 16, 2)
	for by := range 16 {
		for bx := range 32 {
			if bx >= 16 {
				a.Noise[by*32+bx] = float32(math.NaN())
			} else {
				b.Noise[by*32+bx] = float32(math.NaN())
			}
		}
	}
	raw := measureMosaicNoise([]binnedPanel{a, b}, []float64{1, 1})
	if raw.Tiles != 8 || raw.Max != 2 || raw.MaxPanel != 1 || raw.Median != 1 || raw.P90 != 2 {
		t.Errorf("unscaled %+v", raw)
	}
	scaled := measureMosaicNoise([]binnedPanel{a, b}, []float64{1, 2})
	if scaled.Max != 1 || scaled.P90 != 1 {
		t.Errorf("scaled %+v", scaled)
	}
	both := measureMosaicNoise([]binnedPanel{uniformPanel(8, 8, 1), uniformPanel(8, 8, 1)}, []float64{1, 1})
	if math.Abs(both.Median-1/math.Sqrt2) > 1e-9 {
		t.Errorf("overlapping tile %+v", both)
	}
	if empty := measureMosaicNoise(nil, nil); empty.Tiles != 0 || !math.IsNaN(empty.P90) {
		t.Errorf("empty %+v", empty)
	}
}

func TestPanelScalesFromFluxRatios(t *testing.T) {
	t.Parallel()
	s := panelScales(3, []seamMeasure{{I: 0, J: 1, StarMatches: 20, FluxRatio: 2}, {I: 1, J: 2, StarMatches: 20, FluxRatio: 1}, {I: 0, J: 2, StarMatches: 2, FluxRatio: 9}})
	if math.Abs(s[0]/s[1]-2) > 1e-6 || math.Abs(s[1]/s[2]-1) > 1e-6 || math.Abs(s[0]*s[1]*s[2]-1) > 1e-6 {
		t.Errorf("scales %v", s)
	}
	if u := panelScales(2, nil); u[0] != 1 || u[1] != 1 {
		t.Errorf("no stars %v", u)
	}
}

func TestSeamColoursAcrossFilters(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.MosaicSeam{}, &app.MosaicPanelHealth{}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db}
	g := mosaicGroup{Project: rhoProject}
	ctx := context.Background()
	save := func(filter string, ratio float64) {
		t.Helper()
		r := ratio
		row := app.MosaicSeam{Project: rhoProject, Filter: filter, PanelA: 1, PanelB: 2, StarMatches: 20, FluxRatio: &r, OK: true}
		if err := p.saveSeams(ctx, g, filter, []app.MosaicSeam{row}, nil); err != nil {
			t.Fatal(err)
		}
	}
	load := func() map[string]app.MosaicSeam {
		t.Helper()
		var rows []app.MosaicSeam
		if err := db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := map[string]app.MosaicSeam{}
		for _, r := range rows {
			out[r.Filter] = r
		}
		return out
	}
	save("R", 1)
	if r := load()["R"]; r.Colour != nil || r.ColourRef != "" {
		t.Errorf("one filter has a colour: %+v", r)
	}
	save("G", 1)
	save("B", 1.3)
	rows := load()
	b, red := rows["B"], rows["R"]
	if b.Colour == nil || math.Abs(*b.Colour-(math.Pow(1.3, 2.0/3)-1)) > 1e-9 || b.OK || !strings.Contains(b.Problems, colourProblem) {
		t.Errorf("blue %+v", b)
	}
	if red.Colour == nil || *red.Colour > 0 || !red.OK || b.ColourRef != "B,G,R" {
		t.Errorf("red %+v", red)
	}
	save("B", 1)
	if b := load()["B"]; b.Colour == nil || math.Abs(*b.Colour) > 1e-9 || !b.OK || b.Problems != "" {
		t.Errorf("blue after matching %+v", b)
	}
}
