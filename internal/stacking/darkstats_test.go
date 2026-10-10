package stacking

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/darkcheck"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDarkStats(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("DARK_DIR")
	if dir == "" {
		t.Skip("DARK_DIR not set")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, _ := fs.Glob(root.FS(), "*.xisf")
	for _, f := range files {
		b, _ := root.ReadFile(f)
		im, err := imagedata.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		m := darkcheck.Measure(im.Data, im.W, im.H)
		t.Logf("%-60s median %6.1f  noise %5.2f  spread %6.1f ADU", f, m.Median, m.Noise, m.Spread)
	}
}

func testFrame(seed uint64, w, h int, ramp float64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed))
	d := make([]float32, w*h)
	for y := range h {
		for x := range w {
			n := 500 + 7*r.NormFloat64()
			d[y*w+x] = float32((n + ramp*float64(w-x+h-y)/float64(w+h)) / 65535)
		}
	}
	return d
}

func testDarks() (clean, leak []float32, w, h int) {
	w, h = 2048, 1536
	return testFrame(5, w, h, 0), testFrame(6, w, h, 150), w, h
}

func TestDropLeakyDarksRecordsClean(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	clean, leak, w, h := testDarks()
	gain, offset, bin, exp, temp := 100.0, 50.0, 1.0, 300.0, -10.0
	for i := range 5 {
		m := darkcheck.Measure(testFrame(uint64(20+i), w, h, 0), w, h)
		now := time.Now()
		typ, key, check := "BIAS", fmt.Sprintf("bias%d.fit", i), (*string)(nil)
		if i >= 3 {
			c := darkcheck.StateClean
			typ, key, check = frameTypeDark, fmt.Sprintf("ref%d.fit", i), &c
		}
		b := app.Frame{Key: key, Type: typ, Gain: &gain, Offset: &offset, BinX: &bin, Exposure: &exp, SetTemp: &temp,
			CalMedianADU: &m.Median, CalSpreadADU: &m.Spread, CalNoiseADU: &m.Noise, CalSpreadErrADU: &m.SpreadErr, CalMeasuredAt: &now, CalCheck: check}
		if err := db.Create(&b).Error; err != nil {
			t.Fatal(err)
		}
	}
	taken := time.Now()
	dir := t.TempDir()
	darks := [][]float32{clean, leak}
	frames := make([]app.Frame, 0, len(darks))
	files := make([]string, 0, len(darks))
	for i, data := range darks {
		f := app.Frame{Key: []string{"clean.fit", "leak.fit"}[i], Type: frameTypeDark, Gain: &gain, Offset: &offset, BinX: &bin, Exposure: &exp, SetTemp: &temp, DateObs: &taken}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
		files = append(files, filepath.Join(dir, f.Key))
		if err := writeFITSFile(files[i], w, h, 1, data, nil); err != nil {
			t.Fatal(err)
		}
	}
	opts := DefaultPipelineOptions()
	opts.RejectDarksSince = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, dir, opts)
	left, err := p.dropLeakyDarks(context.Background(), frames, files)
	if err != nil || left != 1 {
		t.Fatalf("%d left, %v", left, err)
	}
	var got []app.Frame
	db.Order("id").Find(&got)
	if got[5].DarkSpread == nil || got[5].LightLeak != nil || got[5].CalCheck == nil || *got[5].CalCheck != darkcheck.StateClean {
		t.Errorf("clean dark: spread %v, leak %v, check %v", got[5].DarkSpread, got[5].LightLeak, got[5].CalCheckReason)
	}
	if got[6].DarkSpread == nil || got[6].LightLeak == nil || *got[6].LightLeak != *got[6].DarkSpread || *got[6].CalCheck != darkcheck.StateLeak {
		t.Errorf("leaky dark: spread %v, leak %v", got[6].DarkSpread, got[6].LightLeak)
	}
	if _, err := os.Stat(files[1]); !os.IsNotExist(err) {
		t.Error("leaky dark not removed")
	}
}
