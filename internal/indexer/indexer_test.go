package indexer

import (
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/measure"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Lights without a pointing are read again when a new way of finding one
// comes (PointingRevision), once; lights with one, or read at the current
// revision, are left alone.
func TestPointingUnread(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	ra := 10.0
	for _, f := range []app.Frame{
		{Key: "nina", MountRA: &ra, MountDec: &ra, PointingRead: true},
		{Key: "telescope-live-old", PointingRead: true},
		{Key: "telescope-live-read", PointingRead: true, PointingWCS: true},
		{Key: "telescope-live-current", PointingRead: true, PointingWCS: true, PointingRev: PointingRevision},
		{Key: "never-read"},
	} {
		f.Type, f.LastModified = "LIGHT", time.Now()
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	if err := pointingUnread(db).Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "never-read" || keys[1] != "telescope-live-old" || keys[2] != "telescope-live-read" {
		t.Errorf("read again %v, want never-read, telescope-live-old and telescope-live-read", keys)
	}
}

// Every light is photometered once at the current revision, except those no
// master takes whatever their score.
func TestPhotometryPending(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	old, cur := measure.PhotometryRevision-1, measure.PhotometryRevision
	for _, c := range []struct {
		key    string
		rev    *int
		status string
	}{
		{"new", nil, ""}, {"old-rev", &old, app.StackStatusAdded}, {"measured", &cur, app.StackStatusAdded},
		{"low", nil, app.StackStatusLowScore}, {"elsewhere", nil, app.StackStatusOffTarget},
		{"twice", nil, app.StackStatusDuplicate}, {"dead", nil, app.StackStatusDead},
	} {
		f := app.Frame{Key: c.key, Type: "LIGHT", LastModified: time.Now(), PhotometryRev: c.rev}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
		if c.status != "" {
			if err := db.Create(&app.StackFrame{FrameID: f.ID, Status: c.status}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.Create(&app.Frame{Key: "flat", Type: "FLAT", LastModified: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := photometryPending(db).Order("key").Pluck("key", &keys).Error; err != nil {
		t.Fatal(err)
	}
	if want := []string{"low", "new", "old-rev"}; !slices.Equal(keys, want) {
		t.Errorf("pending %v, want %v", keys, want)
	}
}
