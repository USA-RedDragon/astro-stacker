package quality_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestParseMetadataHandlesNaNStrings(t *testing.T) {
	t.Parallel()
	m, err := quality.ParseMetadata(`{"FileName":"A:\\NINA\\M31\\LIGHT\\x.xisf","FilterName":"Red",` +
		`"ExposureDuration":600.0,"DetectedStars":349,"HFR":1.57,"FWHM":"NaN","ADUMedian":620.0}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !math.IsNaN(float64(m.FWHM)) {
		t.Errorf("FWHM = %v, want NaN", m.FWHM)
	}
	if m.HFR != 1.57 || m.ADUMedian != 620 || m.ExposureDuration != 600 || m.FilterName != "Red" {
		t.Errorf("unexpected metadata: %+v", m)
	}
}

func TestParseMetadataRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := quality.ParseMetadata(`not json`); err == nil {
		t.Error("expected an error")
	}
}

func TestSky(t *testing.T) {
	t.Parallel()
	if got := quality.Sky(620, 506); got != 114 {
		t.Errorf("Sky = %v, want 114", got)
	}
	if got := quality.Sky(500, 506); !math.IsNaN(got) {
		t.Errorf("Sky below pedestal = %v, want NaN", got)
	}
}

func TestRawWeight(t *testing.T) {
	t.Parallel()
	// Doubling sky halves the weight; doubling HFR costs 16x.
	base := quality.RawWeight(100, 2)
	if got := quality.RawWeight(200, 2) / base; math.Abs(got-0.5) > 1e-12 {
		t.Errorf("sky ratio = %v, want 0.5", got)
	}
	if got := quality.RawWeight(100, 4) / base; math.Abs(got-1.0/16) > 1e-12 {
		t.Errorf("hfr ratio = %v, want 1/16", got)
	}
	if !math.IsNaN(quality.RawWeight(math.NaN(), 2)) || !math.IsNaN(quality.RawWeight(100, 0)) {
		t.Error("missing inputs should give NaN")
	}
}

func TestReferenceAndScore(t *testing.T) {
	t.Parallel()
	ws := []float64{math.NaN()}
	for i := 1; i <= 11; i++ {
		ws = append(ws, float64(i))
	}
	ref := quality.Reference(ws) // p90 of 1..11 = 10
	if math.Abs(ref-10) > 1e-12 {
		t.Fatalf("Reference = %v, want 10", ref)
	}
	if got := quality.Score(5, ref); got != 0.5 {
		t.Errorf("Score = %v, want 0.5", got)
	}
	if got := quality.Score(11, ref); got != 1 {
		t.Errorf("Score above reference = %v, want capped at 1", got)
	}
	if got := quality.Score(math.NaN(), ref); got != 0 {
		t.Errorf("Score of missing weight = %v, want 0", got)
	}
	if !math.IsNaN(quality.Reference(nil)) {
		t.Error("Reference of empty group should be NaN")
	}
}

func TestPedestalAt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ offset, want float64 }{
		{50, 506}, {240, 2406}, {0, 506}, {math.NaN(), 506},
	} {
		if got := quality.PedestalAt(506, c.offset); got != c.want {
			t.Errorf("PedestalAt(506, %v) = %v, want %v", c.offset, got, c.want)
		}
	}
}

// Subs that came calibrated have no pedestal left to take off their sky.
func TestCalibratedSubsScore(t *testing.T) {
	t.Parallel()
	scores, err := quality.LoadScores(t.Context(), emptyScheduler(t), 506, []quality.Measured{
		{File: "a_cal.fits", Target: "Crescent", Filter: "H-a", Exposure: 600, SkyADU: 177, HFR: 2.5, Calibrated: true},
		{File: "b_cal.fits", Target: "Crescent", Filter: "H-a", Exposure: 600, SkyADU: 180, HFR: 2.6, Calibrated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := scores["a_cal.fits"]; !(s.Score > 0) {
		t.Errorf("calibrated sub scored %+v, want above 0", s)
	}
}

// emptyScheduler is a scheduler database without acquired images.
func emptyScheduler(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE target ("Id" integer, name text)`,
		`CREATE TABLE acquiredimage ("Id" integer, "targetId" integer, "gradingStatus" integer, metadata text)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}
