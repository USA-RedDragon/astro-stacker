package server

import (
	"context"
	"testing"

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
	if s := byFile["a.fits"]; s.Status != app.StackStatusAdded || s.Score != 0.8 || s.Weight != 240 || s.Photometry != PhotometryMeasured || s.ProcessedAt == nil {
		t.Errorf("a.fits: %+v", s)
	}
	if s := byFile["b.fits"]; s.Status != app.StackStatusOffTarget || s.Error != msg || s.Weight != 0 || s.Photometry != PhotometryFailed {
		t.Errorf("b.fits: %+v", s)
	}
	if s := byFile["c.fits"]; s.Status != "" || s.Score != 0 || s.ProcessedAt != nil || s.Photometry != PhotometryPending {
		t.Errorf("c.fits: %+v", s)
	}
}
