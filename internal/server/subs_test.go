package server

import (
	"context"
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestObjectSubs(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	exp := 300.0
	rev := 1
	phot := `{"flux":[1]}`
	broken := "bad header"
	frames := []app.Frame{
		{Key: "lights/Orion/a.fits", Type: lightType, Object: objectOrion, Filter: "L", Exposure: &exp, PhotometryRev: &rev, Photometry: &phot},
		{Key: "lights/Orion/b.fits", Type: lightType, Object: objectOrion, Filter: "L", Exposure: &exp, PhotometryRev: &rev},
		{Key: "lights/Orion/c.fits", Type: lightType, Object: objectOrion, Filter: "L", Exposure: &exp},
		{Key: "lights/Orion/d.fits", Type: lightType, Object: objectOrion, Filter: "L", IndexError: &broken},
		{Key: "lights/M31/e.fits", Type: lightType, Object: objectM31, Filter: "L"},
		{Key: "flats/Orion/f.fits", Type: "FLAT", Object: objectOrion, Filter: "L"},
	}
	for i := range frames {
		frames[i].ETag = frames[i].Key
		if err := db.Create(&frames[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	msg := "pointed 2.1° off target"
	for _, sf := range []app.StackFrame{
		{FrameID: frames[0].ID, Status: app.StackStatusAdded, Score: 0.8, Weight: 240},
		{FrameID: frames[1].ID, Status: app.StackStatusOffTarget, Score: 0.5, Error: &msg},
	} {
		if err := db.Create(&sf).Error; err != nil {
			t.Fatal(err)
		}
	}

	subs, err := objectSubs(context.Background(), db, objectOrion)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("got %d subs, want 3 (lights of Orion that indexed): %+v", len(subs), subs)
	}
	byFile := map[string]Sub{}
	for _, s := range subs {
		byFile[s.File] = s
	}
	if s := byFile["a.fits"]; s.Status != app.StackStatusAdded || s.Score == nil || *s.Score != 0.8 || s.Weight == nil || *s.Weight != 240 || s.Photometry != PhotometryMeasured || s.ProcessedAt == nil {
		t.Errorf("a.fits: %+v", s)
	}
	if s := byFile["b.fits"]; s.Status != app.StackStatusOffTarget || s.Error != msg || s.Weight == nil || *s.Weight != 0 || s.Photometry != PhotometryFailed {
		t.Errorf("b.fits: %+v", s)
	}
	if s := byFile["c.fits"]; s.Status != "" || s.Score != nil || s.Weight != nil || s.ProcessedAt != nil || s.Photometry != PhotometryPending {
		t.Errorf("c.fits: %+v", s)
	}
}

type fakeScorer map[string]quality.SubScore

func (fakeScorer) Restack(context.Context, string) error { return nil }

func (f fakeScorer) Scores(context.Context) (map[string]quality.SubScore, error) { return f, nil }

func TestWithScoring(t *testing.T) {
	t.Parallel()
	fileA := "a.fits"
	subs := []Sub{{File: fileA}, {File: "b.fits"}, {File: "c.fits"}}
	scorer := fakeScorer{
		fileA: {TargetBest: 0.8, Reference: 2e-6, ReferenceSubs: 40, Transparency: 0.9, TransparencySource: quality.TransparencyPhotometry,
			Pedestal: quality.Pedestal{ADU: 503, Source: quality.PedestalBias, Basis: "median of the 2025-01-19 master bias"}, Sky: 80},
		"b.fits": {TargetBest: 0.8, Reference: math.NaN(), Transparency: 1, TransparencyMissing: "no star photometry of the sub",
			Pedestal: quality.Pedestal{ADU: 506, Source: quality.PedestalConfigured}, Sky: math.NaN(), Missing: "no HFR"},
	}
	withScoring(context.Background(), subs, scorer, 0.3)
	a := subs[0].Scoring
	if a == nil || *a.Cut != 0.3*0.8 || *a.Transparency != 0.9 || *a.PedestalADU != 503 || *a.SkyADU != 80 || a.ReferenceSubs != 40 {
		t.Errorf("a: %+v", a)
	}
	b := subs[1].Scoring
	if b == nil || b.Transparency != nil || b.TransparencyMissing == "" || b.SkyADU != nil || b.ReferenceWeight != nil || b.Unmeasured != "no HFR" {
		t.Errorf("b: %+v", b)
	}
	if subs[2].Scoring != nil || subs[2].NoScoring == "" {
		t.Errorf("c: %+v", subs[2])
	}
	off := []Sub{{File: fileA}}
	withScoring(context.Background(), off, nil, 0.3)
	if off[0].Scoring != nil || off[0].NoScoring != "stacking is off" {
		t.Errorf("stacking off: %+v", off[0])
	}
}
