package rigsource_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const light = "LIGHT"

func open(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMeasureReadsTheRigFromRecentLights(t *testing.T) {
	t.Parallel()
	appDB, sched := open(t), open(t)
	if err := appDB.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	rev := 1
	add := func(i int, camera, filter string, exp float64, w, h int, fl, px float64, bayer string, age time.Duration) {
		t.Helper()
		d := now.Add(-age)
		f := app.Frame{Key: fmt.Sprintf("f%d", i), ETag: "e", Type: light, Camera: camera, Filter: filter, Exposure: &exp, DateObs: &d, LastModified: d,
			Width: &w, Height: &h, FocalLength: &fl, PixelSize: &px, GeometryRev: &rev}
		if bayer != "" {
			f.BayerPattern = &bayer
		}
		scope := "FRA400"
		f.Telescope = &scope
		if err := appDB.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	i := 0
	for range 6 {
		add(i, "ASI2600MM", "H-a", 600, 6248, 4176, 405, 3.76, "", time.Hour)
		i++
	}
	for range 4 {
		add(i, "ASI2600MM", "Luminance", 120, 6248, 4176, 404, 3.76, "", 2*time.Hour)
		i++
	}
	add(i, "ASI2600MM", "Red", 300, 6248, 4176, 405, 3.76, "", time.Hour)
	add(i+1, "ASI2600MC", "", 60, 3000, 2000, 250, 3.76, "RGGB", time.Hour)
	add(i+2, "ASI2600MM", "O-III", 600, 6248, 4176, 405, 3.76, "", 90*24*time.Hour)
	if err := sched.Exec(`CREATE TABLE acquiredimage (Id INTEGER PRIMARY KEY, acquireddate INTEGER, metadata TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	for k, hfr := range []float64{1.0, 1.2, 1.4} {
		ticks := now.Add(-time.Hour).Unix()*10000000 + 621355968000000000
		meta := fmt.Sprintf(`{"HFR":%v,"GuidingRMSArcSec":%v}`, hfr, 0.5+0.1*float64(k))
		if err := sched.Exec(`INSERT INTO acquiredimage (acquireddate, metadata) VALUES (?, ?)`, ticks, meta).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := sched.Exec(`INSERT INTO acquiredimage (acquireddate, metadata) VALUES (?, ?)`, now.Add(-100*24*time.Hour).Unix(), `{"HFR":9,"GuidingRMSArcSec":9}`).Error; err != nil {
		t.Fatal(err)
	}
	r, err := rigsource.Measure(context.Background(), appDB, sched, now)
	if err != nil {
		t.Fatal(err)
	}
	checkMeasured(t, r)
}

func checkMeasured(t *testing.T, r rigsource.Rig) {
	t.Helper()
	if !r.Known() || *r.FocalLength != 405 || *r.PixelSize != 3.76 || *r.WidthPx != 6248 || *r.HeightPx != 4176 {
		t.Fatalf("rig %+v", r)
	}
	if *r.Basis.Camera != "ASI2600MM" || r.Basis.Frames != 11 || *r.Basis.Telescope != "FRA400" || r.Basis.Source != rigsource.SourceHeaders {
		t.Errorf("basis %+v", r.Basis)
	}
	if r.Colour == nil || *r.Colour {
		t.Errorf("colour %v", r.Colour)
	}
	if fmt.Sprint(r.Filters) != "[L H]" || r.Exposures["H"] != 600 || r.Exposures["L"] != 120 || r.Exposures["R"] != 0 {
		t.Errorf("filters %v exposures %v", r.Filters, r.Exposures)
	}
	scale := 206.264806 * 3.76 / 405
	if math.Abs(*r.Scale-scale) > 1e-9 || math.Abs(*r.WidthDeg-scale*6248/3600) > 1e-9 {
		t.Errorf("scale %v width %v", *r.Scale, *r.WidthDeg)
	}
	if r.TypicalHFR == nil || math.Abs(*r.TypicalHFR-1.2*scale) > 1e-9 || r.Basis.HFRFrames != 3 || *r.Basis.HFRSource != "target-scheduler" {
		t.Errorf("hfr %v basis %+v", r.TypicalHFR, r.Basis)
	}
	if r.TypicalGuideRMS == nil || math.Abs(*r.TypicalGuideRMS-0.6) > 1e-9 || r.Basis.GuideFrames != 3 {
		t.Errorf("guiding %v", r.TypicalGuideRMS)
	}
	m, ok := r.Mosaic()
	if !ok || m.ScaleArcsec != *r.Scale || m.WidthDeg != *r.WidthDeg {
		t.Errorf("mosaic rig %+v", m)
	}
}

func TestMeasureWithoutLightsIsUnknown(t *testing.T) {
	t.Parallel()
	appDB := open(t)
	if err := appDB.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	r, err := rigsource.Measure(context.Background(), appDB, nil, time.Now())
	if err != nil || r.Known() || r.Basis.Source != rigsource.SourceNone || r.Filters == nil || r.Exposures == nil || r.Colour != nil {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := rigsource.MosaicRig(context.Background(), appDB); err == nil {
		t.Error("an unknown rig framed a mosaic")
	}
	src := &rigsource.Source{App: appDB}
	if src.Get(context.Background()).Known() {
		t.Error("source invented a rig")
	}
	if rigsource.CanonicalFilter("Halpha") != "H" || rigsource.CanonicalFilter("O-III") != "O" || rigsource.CanonicalFilter("Luminance") != "L" {
		t.Error("canonical filters")
	}
}

func TestMeasureFallsBackToOlderLights(t *testing.T) {
	t.Parallel()
	appDB := open(t)
	if err := appDB.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-400 * 24 * time.Hour)
	fl, px, w, h := 530.0, 4.63, 4144, 2822
	if err := appDB.Create(&app.Frame{Key: "old", ETag: "e", Type: light, DateObs: &old, LastModified: old, FocalLength: &fl, PixelSize: &px, Width: &w, Height: &h}).Error; err != nil {
		t.Fatal(err)
	}
	r, err := rigsource.Measure(context.Background(), appDB, nil, now)
	if err != nil || !r.Known() || *r.FocalLength != 530 || r.Colour != nil || r.TypicalHFR != nil {
		t.Fatalf("%+v %v", r, err)
	}
}
