package stacking

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestWorkersClaimDifferentTargets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, f := range []struct{ object, filter string }{
		{"A", "Red"}, {"A", "Blue"}, {"B", "Red"}, {"C", "H-a"},
	} {
		d := now.Add(time.Duration(i) * time.Minute)
		if err := db.Create(&app.Frame{Key: f.object + f.filter, Type: "LIGHT", Object: f.object, Filter: f.filter,
			LastModified: now, DateObs: &d}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db, busy: map[string]bool{}}
	lights := func() *gorm.DB {
		return db.Model(&app.Frame{}).Joins("LEFT JOIN stack_frames sf ON sf.frame_id = frames.id").
			Where("frames.type = ? AND sf.id IS NULL", "LIGHT")
	}
	var got []string
	for range 4 {
		f, err := p.claim(lights())
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			got = append(got, "-")
			continue
		}
		got = append(got, f.Object)
	}
	// A's second filter must wait for A to be released.
	if want := []string{"A", "B", "C", "-"}; !slices.Equal(got, want) {
		t.Fatalf("claimed %v, want %v", got, want)
	}
	p.release("A")
	if f, _ := p.claim(lights()); f == nil || f.Object != "A" {
		t.Fatalf("after release claimed %v, want A", f)
	}
}

func TestFailuresBackOffThenDie(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions)
	ctx := context.Background()
	var waits []time.Duration
	for range 5 {
		if err := p.record(ctx, app.StackFrame{FrameID: 1, Status: app.StackStatusFailed}); err != nil {
			t.Fatal(err)
		}
		var sf app.StackFrame
		db.Where("frame_id = 1").First(&sf)
		if sf.NextAttemptAt != nil {
			waits = append(waits, sf.NextAttemptAt.Sub(sf.ProcessedAt).Round(time.Minute))
		} else if sf.Status != app.StackStatusDead || sf.Attempts != 5 {
			t.Fatalf("after 5 failures: %s, %d attempts", sf.Status, sf.Attempts)
		}
	}
	want := []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour}
	if !slices.Equal(waits, want) {
		t.Errorf("waits %v, want %v", waits, want)
	}
	// Success clears the failure count.
	if err := p.record(ctx, app.StackFrame{FrameID: 1, Status: app.StackStatusAdded}); err != nil {
		t.Fatal(err)
	}
	var sf app.StackFrame
	db.Where("frame_id = 1").First(&sf)
	if sf.Attempts != 0 || sf.NextAttemptAt != nil {
		t.Errorf("after success: %d attempts, next %v", sf.Attempts, sf.NextAttemptAt)
	}
}
