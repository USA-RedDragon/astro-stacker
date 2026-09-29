package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func duplicateDB(t *testing.T) (*gorm.DB, []app.Frame) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	// Telescope.live's twins: the same ETag and size under two names. The
	// same ETag on another target, or with another size, is another file.
	frames := []app.Frame{
		{Key: "Rho/ID224158_cal.fits", Object: "Rho Ophiuchi", ETag: "aaa", Size: 100},
		{Key: "Rho/ID224159_cal.fits", Object: "Rho Ophiuchi", ETag: "aaa", Size: 100},
		{Key: "Rho/ID224160_cal.fits", Object: "Rho Ophiuchi", ETag: "bbb", Size: 100},
		{Key: "Rho/ID224161_cal.fits", Object: "Rho Ophiuchi", ETag: "bbb", Size: 101},
		{Key: "Other/ID1_cal.fits", Object: "Other", ETag: "aaa", Size: 100},
	}
	for i := range frames {
		frames[i].Type, frames[i].Filter, frames[i].LastModified = "LIGHT", "Red", time.Now()
		if err := db.Create(&frames[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, frames
}

// Only the later of two identical files of a target is a duplicate.
func TestDuplicates(t *testing.T) {
	t.Parallel()
	db, frames := duplicateDB(t)
	p := &Pipeline{db: db}
	dups, err := p.duplicates(context.Background(), frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 1 || !dups[frames[1].ID] {
		t.Errorf("duplicates %v, want only %s (frame %d)", dups, frames[1].Key, frames[1].ID)
	}
}

// A duplicate already in a master is taken out and the master marked for
// the moon sweep to stack again; when its twin goes, it is stacked again.
func TestDropDuplicates(t *testing.T) {
	t.Parallel()
	db, frames := duplicateDB(t)
	stack := app.Stack{Object: "Rho Ophiuchi", Filter: "Red"}
	other := app.Stack{Object: "Other", Filter: "Red"}
	for _, s := range []*app.Stack{&stack, &other} {
		if err := db.Create(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, f := range frames {
		id := stack.ID
		if f.Object == "Other" {
			id = other.ID
		}
		if err := db.Create(&app.StackFrame{ID: 100 + i, FrameID: f.ID, StackID: &id, Status: app.StackStatusAdded}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{db: db}
	if err := p.dropDuplicates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sfs []app.StackFrame
	if err := db.Order("id").Find(&sfs).Error; err != nil {
		t.Fatal(err)
	}
	for i, sf := range sfs {
		want := app.StackStatusAdded
		if i == 1 {
			want = app.StackStatusDuplicate
		}
		if sf.Status != want || sf.NextAttemptAt != nil {
			t.Errorf("%s: status %q (next %v), want %q", frames[i].Key, sf.Status, sf.NextAttemptAt, want)
		}
	}
	for _, s := range []app.Stack{stack, other} {
		if err := db.First(&s, s.ID).Error; err != nil {
			t.Fatal(err)
		}
		if s.NeedsRebuild != (s.ID == stack.ID) {
			t.Errorf("%s needs_rebuild %v", s.Object, s.NeedsRebuild)
		}
	}

	// The twin leaves the bucket: the duplicate is decided again.
	if err := db.Delete(&frames[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.dropDuplicates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := db.Model(&app.StackFrame{}).Where("frame_id = ?", frames[1].ID).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Error("a duplicate whose twin is gone is still left out")
	}
}
