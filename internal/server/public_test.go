package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPublicFrame(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.StackFrame{}, &app.PublicFrame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, f := range []struct {
		object string
		status string
		age    time.Duration
	}{
		{"Orion", app.StackStatusAdded, 2 * time.Hour},
		{"M 31", app.StackStatusAdded, time.Hour},
		// Rescored out of its master since it was rendered: never served.
		{"Leo Triplet", app.StackStatusLowScore, time.Minute},
	} {
		id := i + 1
		at := now.Add(-f.age)
		if err := db.Create(&app.StackFrame{FrameID: id, Status: f.status}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app.PublicFrame{Object: f.object, FrameID: id, Key: f.object + ".jpg", ETag: `"e"`, DateObs: &at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for object, want := range map[string]string{"": "M 31", "Orion": "Orion"} {
		f, err := publicFrame(ctx, db, object)
		if err != nil || f.Object != want {
			t.Errorf("publicFrame(%q) = %q, %v; want %q", object, f.Object, err, want)
		}
	}
	for _, object := range []string{"Leo Triplet", "Rosette"} {
		if _, err := publicFrame(ctx, db, object); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Errorf("publicFrame(%q): %v, want not found", object, err)
		}
	}
}

func TestPublicSize(t *testing.T) {
	for _, tc := range []struct {
		w, h string
		ok   bool
	}{{"", "", true}, {"800", "480", true}, {"800", "", true}, {"1600", "960", false}, {"x", "", false}} {
		if got := publicSize(tc.w, tc.h); got != tc.ok {
			t.Errorf("publicSize(%q, %q) = %v", tc.w, tc.h, got)
		}
	}
}
