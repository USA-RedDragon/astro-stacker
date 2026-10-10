package coverage_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	darkKey300 = "offset240/masters/masterDark_BIN-1_6248x4176_-20.00-EXPOSURE-300.00s.xisf"
	darkKey600 = "offset240/masters/masterDark_BIN-1_6248x4176_-20.00-EXPOSURE-600.00s.xisf"
	biasKey    = "offset240/masters/masterBias_BIN-1_6248x4176.xisf"
)

func TestImportedCalibrateOffset240(t *testing.T) {
	t.Parallel()
	night := time.Date(2025, 2, 4, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		gain, exposure, temp float64
		dark                 string
	}{
		{100, 600, -10, "600.00s"},
		{0, 300, -15, "300.00s"},
		{0, 120, -20, "300.00s"},
	} {
		r := calmatch.Choose(calmatch.Group{Night: night, Filter: "H-a", Exposure: c.exposure, Gain: c.gain,
			Offset: 240, SetTemp: c.temp, BinX: 1, Rotator: math.NaN()}, coverage.Imported())
		if r.Bias.Set == nil || r.Bias.Set.Master == "" {
			t.Errorf("gain %v: no bias", c.gain)
		}
		if r.Dark.Set == nil || !strings.Contains(r.Dark.Set.Master, c.dark) {
			t.Errorf("gain %v %vs: dark %+v, want the %s one", c.gain, c.exposure, r.Dark.Set, c.dark)
		}
	}
}

func TestImportedFromKey(t *testing.T) {
	t.Parallel()
	for _, s := range coverage.Imported() {
		if s.Offset != 240 || s.BinX != 1 {
			t.Errorf("%s: offset %v bin %v", s.Master, s.Offset, s.BinX)
		}
		if s.Type == typeDark && (s.SetTemp != -20 || (s.Exposure != 300 && s.Exposure != 600)) {
			t.Errorf("%s: set temp %v exposure %v", s.Master, s.SetTemp, s.Exposure)
		}
	}
}

func importedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestImportedSetsReadHeaders(t *testing.T) {
	t.Parallel()
	db := importedDB(t)
	msg := "not an XISF or FITS file"
	for i, fr := range []app.Frame{
		{Key: darkKey600, Type: "MASTERDARK", Exposure: f(600), SetTemp: f(-19), Night: day("2025-01-22")},
		{Key: biasKey, Type: "MASTERBIAS", IndexError: &msg},
	} {
		fr.ETag, fr.LastModified = fmt.Sprint(i), time.Now()
		if err := db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
	}
	sets, err := coverage.ImportedSets(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 4 {
		t.Fatalf("got %d sets", len(sets))
	}
	for _, s := range sets {
		switch s.Master {
		case darkKey600:
			if s.SetTemp != -19 || s.Basis.SetTemp != coverage.BasisHeader || s.Basis.Exposure != coverage.BasisHeader ||
				s.Night.Format("2006-01-02") != "2025-01-22" || s.Basis.Night != coverage.BasisHeader ||
				s.Gain != 100 || s.Basis.Gain != coverage.BasisHandEntered || s.Basis.Offset != coverage.BasisFolderName {
				t.Errorf("600 s dark %+v", s)
			}
		case darkKey300:
			if s.HeaderError != "master not indexed" || s.Basis.SetTemp != coverage.BasisFileName ||
				s.Basis.Exposure != coverage.BasisFileName || s.Basis.Night != coverage.BasisHandEntered {
				t.Errorf("300 s dark %+v", s)
			}
		case biasKey:
			if s.HeaderError != msg || s.Basis.Gain != coverage.BasisHandEntered || s.Basis.SetTemp != "" {
				t.Errorf("bias %+v", s)
			}
		}
	}
}

func TestReportMarksImported(t *testing.T) {
	t.Parallel()
	db := importedDB(t)
	for i := range 3 {
		fr := app.Frame{Key: fmt.Sprintf("l%d", i), ETag: "e", LastModified: time.Now(), Type: typeLight, Object: objM31,
			Filter: "H-a", Exposure: f(600), Gain: f(100), Offset: f(240), BinX: f(1), Night: day("2025-02-03")}
		if i == 2 {
			fr.Object = "NoTemp"
		} else {
			fr.SetTemp = f(-10)
		}
		if err := db.Create(&fr).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, err := coverage.Report(context.Background(), db, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, r := range rows {
		d := r.Dark
		if d.Source != coverage.SourceImported || d.Master != darkKey600 || d.Basis == nil || d.Basis.Gain != coverage.BasisHandEntered ||
			d.Exposure == nil || *d.Exposure != 600 {
			t.Errorf("%s dark %+v", r.Object, d)
		}
		if r.Bias.Source != coverage.SourceImported || r.Bias.Master != biasKey {
			t.Errorf("%s bias %+v", r.Object, r.Bias)
		}
		switch r.Object {
		case objM31:
			if d.TempOff == nil || *d.TempOff != 10 {
				t.Errorf("temp off %v", d.TempOff)
			}
		case "NoTemp":
			if d.TempOff != nil {
				t.Errorf("unknown setpoint reported %v °C off", *d.TempOff)
			}
		}
	}
}
