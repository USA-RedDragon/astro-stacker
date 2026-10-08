package stacking

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestDarkStats prints the light-leak measure of real darks in DARK_DIR.
func TestDarkStats(t *testing.T) {
	dir := os.Getenv("DARK_DIR")
	if dir == "" {
		t.Skip("DARK_DIR not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.xisf"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		spread, noise := darkSpread(im.Data, im.W, im.H)
		t.Logf("%-60s noise %5.2f  spread %6.1f ADU  leaky %v", filepath.Base(f), noise, spread, leaky(spread))
	}
}

// testDarks are a clean dark, one with light reaching the sensor and one
// with a faint dawn ramp, well under the pixel noise.
func testDarks() (clean, leak, dawn []float32, w, h int) {
	w, h = 2048, 1536
	r := rand.New(rand.NewPCG(5, 5))
	clean = make([]float32, w*h)
	leak = make([]float32, w*h)
	dawn = make([]float32, w*h)
	for y := range h {
		for x := range w {
			n := 500 + 7*r.NormFloat64()
			ramp := float64(w-x+h-y) / float64(w+h) // brighter towards the top left
			clean[y*w+x] = float32(n / 65535)
			leak[y*w+x] = float32((n + 150*ramp) / 65535)
			dawn[y*w+x] = float32((n + 6*ramp) / 65535)
		}
	}
	return clean, leak, dawn, w, h
}

func TestLightLeak(t *testing.T) {
	t.Parallel()
	clean, leak, dawn, w, h := testDarks()
	if s, _ := darkSpread(clean, w, h); leaky(s) {
		t.Errorf("clean dark: spread %v called leaky", s)
	}
	if s, _ := darkSpread(leak, w, h); !leaky(s) {
		t.Errorf("leaky dark: spread %v called clean", s)
	}
	if s, _ := darkSpread(dawn, w, h); !leaky(s) {
		t.Errorf("dawn dark: spread %v called clean", s)
	}
}

// Every dark checked records its measure, so a clean one can be told from
// one never checked; only the leaky one is left out.
func TestDropLeakyDarksRecordsClean(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	clean, leak, _, w, h := testDarks()
	dir := t.TempDir()
	var frames []app.Frame
	var files []string
	for i, data := range [][]float32{clean, leak} {
		f := app.Frame{Key: []string{"clean.fit", "leak.fit"}[i], Type: "DARK"}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
		files = append(files, filepath.Join(dir, f.Key))
		if err := writeFITSFile(files[i], w, h, 1, data, nil); err != nil {
			t.Fatal(err)
		}
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, dir, DefaultPipelineOptions())
	left, err := p.dropLeakyDarks(context.Background(), frames, files)
	if err != nil || left != 1 {
		t.Fatalf("%d left, %v", left, err)
	}
	var got []app.Frame
	db.Order("id").Find(&got)
	if got[0].DarkSpread == nil || got[0].LightLeak != nil {
		t.Errorf("clean dark: spread %v, leak %v", got[0].DarkSpread, got[0].LightLeak)
	}
	if got[1].DarkSpread == nil || got[1].LightLeak == nil || *got[1].LightLeak != *got[1].DarkSpread {
		t.Errorf("leaky dark: spread %v, leak %v", got[1].DarkSpread, got[1].LightLeak)
	}
	if _, err := os.Stat(files[1]); !os.IsNotExist(err) {
		t.Error("leaky dark not removed")
	}
}
