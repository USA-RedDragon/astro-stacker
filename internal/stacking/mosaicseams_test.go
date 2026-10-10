package stacking

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func writeSeamPanels(t *testing.T, noise [2]float64, offset float64) []string {
	t.Helper()
	rng := rand.New(rand.NewPCG(3, 4))
	panels := []registeredPanel{{0, 0, 478, 220}, {2, 140, 478, 220}}
	dir := t.TempDir()
	files := make([]string, 0, len(panels))
	for k, p := range panels {
		data := make([]float32, p.W*p.H)
		for y := range p.H {
			for x := range p.W {
				r := y + p.Y
				v := 0.01 + 0.002*float64(r)/canvasH + noise[k]*rng.NormFloat64()
				if k == 1 {
					v += offset
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

func TestSeamsMeasureNoiseMismatchWithoutRewriting(t *testing.T) {
	t.Parallel()
	files := writeSeamPanels(t, [2]float64{1e-4, 2e-4}, 0.003)
	orig, err := os.ReadFile(files[1])
	if err != nil {
		t.Fatal(err)
	}
	_, after, rep, err := matchRegisteredSeams(files, 0.9, false)
	if err != nil {
		t.Fatal(err)
	}
	seams := rep.seams
	if len(after) != 1 || len(seams) != 1 {
		t.Fatalf("pairs %+v seams %+v", after, seams)
	}
	m := seams[0]
	if r := m.NoiseB / m.NoiseA; math.Abs(r-2) > 0.15 {
		t.Errorf("noise ratio %v, want 2 (%v, %v)", r, m.NoiseA, m.NoiseB)
	}
	if math.Abs(m.NoiseA-1e-4) > 1.5e-5 {
		t.Errorf("noise A %v, want 1e-4", m.NoiseA)
	}
	if len(m.Profile) != seamStrips {
		t.Errorf("profile %v", m.Profile)
	}
	now, err := os.ReadFile(files[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, now) {
		t.Error("measuring seams rewrote a panel")
	}
	rec := seamRecord(rhoProject, "p", "H-a", seamPanel{Number: 1}, seamPanel{Number: 2}, m)
	if rec.OK || !strings.Contains(rec.Problems, "panel 2 is 2.0× noisier") {
		t.Errorf("record %+v", rec)
	}
	if rec.Level > seamLevelWarn {
		t.Errorf("level after matching %v", rec.Level)
	}
}

func TestSeamRecordFlags(t *testing.T) {
	t.Parallel()
	base := seamMeasure{Fit: overlapFit{Mean: 0, Samples: 1000}, NoiseA: 1, NoiseB: 1.1, Profile: []float64{0, 0.05, -0.05, 0}}
	if r := seamRecord("M", "g", "R", seamPanel{Number: 1}, seamPanel{Number: 2}, base); !r.OK || r.Problems != "" || math.Abs(r.Step-0.1/1.05) > 1e-9 {
		t.Errorf("clean seam %+v", r)
	}
	cases := []struct {
		name string
		edit func(*seamMeasure)
		want string
	}{
		{"level", func(m *seamMeasure) { m.Fit.Mean = 0.5 }, "level differs by 0.48σ"},
		{"step", func(m *seamMeasure) { m.Profile = []float64{-0.3, 0, 0.3} }, "gradient step of 0.57σ"},
		{"small", func(m *seamMeasure) { m.Fit.Samples = 250 }, "overlap too small: 250 blocks"},
		{"noisier A", func(m *seamMeasure) { m.NoiseA = 3 }, "panel 1 is 2.7× noisier"},
	}
	for _, c := range cases {
		m := base
		m.Profile = append([]float64(nil), base.Profile...)
		c.edit(&m)
		r := seamRecord("M", "g", "R", seamPanel{Number: 1}, seamPanel{Number: 2}, m)
		if r.OK || !strings.Contains(r.Problems, c.want) {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

func TestSeamRecordsOrderPanelsAndFindGaps(t *testing.T) {
	t.Parallel()
	centre := mosaics.Point{RA: 250, Dec: -24}
	east := mosaics.Offset(centre, 0, -1.4, 0)
	g := mosaicGroup{Project: rhoProject, ProjectGUID: "p", Panels: []panel{
		{Object: "P1", TargetGUID: "t1", Number: 1, RA: centre.RA, Dec: centre.Dec},
		{Object: "P2", TargetGUID: "t2", Number: 2, RA: east.RA, Dec: east.Dec},
	}}
	masters := []app.Stack{{Object: "P2"}, {Object: "P1"}}
	shifted := mosaics.PanelFootprint(mosaics.Offset(east, 0, 0, -0.5), 0, mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915})
	full := mosaics.PanelFootprint(centre, 0, mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915})
	res := seamResult{
		seams:  []seamMeasure{{I: 0, J: 1, Fit: overlapFit{Mean: 0.2, Samples: 900}, NoiseA: 2, NoiseB: 1, Profile: []float64{0.1, -0.1}}},
		actual: []*mosaics.Footprint{&shifted, &full},
	}
	seams, health, _ := seamRecords(g, "H-a", masters, res, "sig", time.Unix(0, 0))
	if len(seams) != 1 || seams[0].PanelA != 1 || seams[0].PanelB != 2 || seams[0].TargetA != "t1" {
		t.Fatalf("seams %+v", seams)
	}
	if seams[0].Difference != -0.2 || seams[0].NoiseA != 1 || seams[0].NoiseB != 2 {
		t.Errorf("seam not turned to panel order: %+v", seams[0])
	}
	if len(health) != 2 || health[0].Panel != 2 || health[0].Noise != 2 {
		t.Fatalf("health %+v", health)
	}
	if health[0].GapFraction < 0.05 || health[0].GapWhere == "" {
		t.Errorf("panel 2 gap %+v", health[0])
	}
	if health[1].GapFraction > 0.01 {
		t.Errorf("panel 1 gap %+v", health[1])
	}
}

func TestSaveSeamsReplacesAProjectFiltersRows(t *testing.T) {
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
	for _, n := range []int{3, 1} {
		seams := make([]app.MosaicSeam, 0, n)
		for i := range n {
			seams = append(seams, app.MosaicSeam{Project: rhoProject, Filter: "R", PanelA: i + 1, PanelB: i + 2})
		}
		if err := p.saveSeams(ctx, g, "R", seams, []app.MosaicPanelHealth{{Project: rhoProject, Filter: "R", Panel: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	db.Model(&app.MosaicSeam{}).Count(&count)
	if count != 1 {
		t.Errorf("%d seams after replacing", count)
	}
	p.seamBudget = 1
	if p.seamsDue() {
		t.Error("seams measured with the switch off")
	}
	p.opts.MosaicSeams = true
	p.busy = map[string]bool{"M31": true}
	if p.seamsDue() {
		t.Error("seams measured while a target is stacking")
	}
	p.busy = nil
	if !p.seamsDue() || p.seamsDue() {
		t.Error("seam budget not spent once")
	}
}
