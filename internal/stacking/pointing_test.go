package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func pointed(id int, ra, dec, hfr float64) candidate {
	return candidate{frame: app.Frame{ID: id, MountRA: &ra, MountDec: &dec},
		score: quality.SubScore{HFR: hfr, Stars: 1000, Score: 1}}
}

func TestOffTargetSubsAreLeftOut(t *testing.T) {
	p := &Pipeline{}
	positions := map[string][2]float64{"Cygnis Loop Panel 2": {313.02, 29.76}}
	exp := 300.0
	ra, dec := 0.04, 0.0002 // parked
	f := app.Frame{Object: "Cygnis Loop Panel 2", MountRA: &ra, MountDec: &dec, Exposure: &exp}
	if _, status := p.classify(f, nil, nil, positions); status != app.StackStatusOffTarget {
		t.Errorf("parked sub: status %q", status)
	}
	// Re-framed by a degree: still on target, left to scoring.
	ra, dec = 313.02, 30.8
	if _, status := p.classify(f, nil, nil, positions); status == app.StackStatusOffTarget {
		t.Error("a sub a degree off was left out as off target")
	}
}

func TestReferenceComesFromTheMainFraming(t *testing.T) {
	// Horsehead: one night framed 1.1° away, and its sharpest sub is there.
	cands := []candidate{
		pointed(1, 84.29, -3.30, 1.2),
		pointed(2, 85.05, -2.46, 1.8),
		pointed(3, 85.06, -2.46, 1.6),
		pointed(4, 85.04, -2.47, 1.7),
	}
	best, ok := pickReference(mainFraming(cands))
	if !ok || best.frame.ID != 3 {
		t.Errorf("picked %d, want 3, the sharpest of the main framing", best.frame.ID)
	}
	// Elongated stars are passed over.
	cands[2].score.Eccentricity = 0.7
	if best, _ := pickReference(mainFraming(cands)); best.frame.ID != 4 {
		t.Errorf("picked %d, want 4 once 3's stars are elongated", best.frame.ID)
	}
}

func TestReReferenceRestacksTheTargetAtMostTwice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}, &app.Stack{}, &app.TargetReference{}, &app.ReferenceReset{}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{db: db, workDir: t.TempDir()}
	fill := func() {
		db.Exec("DELETE FROM frames")
		for i := range 20 {
			f := app.Frame{Key: string(rune('a'+i)) + "h", Type: "LIGHT", Object: "Horsehead", LastModified: time.Now()}
			db.Create(&f)
			status := app.StackStatusAdded
			if i < 12 {
				status = app.StackStatusRegistration
			}
			db.Create(&app.StackFrame{FrameID: f.ID, Status: status})
		}
		other := app.Frame{Key: "other", Type: "LIGHT", Object: "M31", LastModified: time.Now()}
		db.Create(&other)
		db.Create(&app.StackFrame{FrameID: other.ID, Status: app.StackStatusAdded})
		db.Create(&app.Stack{Object: "Horsehead", Filter: "Red"})
		db.Create(&app.TargetReference{Object: "Horsehead", ObjectKey: "k"})
	}
	ctx := context.Background()
	for round := 1; round <= 3; round++ {
		fill()
		replaced, err := p.maybeReReference(ctx, "Horsehead")
		if err != nil {
			t.Fatal(err)
		}
		if want := round <= maxReReferences; replaced != want {
			t.Fatalf("round %d: replaced %v, want %v", round, replaced, want)
		}
		var left int64
		db.Model(&app.StackFrame{}).Joins("JOIN frames f ON f.id = stack_frames.frame_id").Where("f.object = ?", "M31").Count(&left)
		if left != 1 {
			t.Fatalf("round %d: another target's subs were cleared", round)
		}
		db.Exec("DELETE FROM stack_frames WHERE frame_id IN (SELECT id FROM frames WHERE object = 'Horsehead')")
		db.Exec("DELETE FROM stacks")
		db.Exec("DELETE FROM target_references")
		db.Exec("DELETE FROM stack_frames")
	}
	// Few failures don't trigger it.
	db.Exec("DELETE FROM reference_resets")
	db.Exec("DELETE FROM frames")
	f := app.Frame{Key: "x", Type: "LIGHT", Object: "Horsehead", LastModified: time.Now()}
	db.Create(&f)
	db.Create(&app.StackFrame{FrameID: f.ID, Status: app.StackStatusRegistration})
	if replaced, _ := p.maybeReReference(ctx, "Horsehead"); replaced {
		t.Error("one failure replaced the reference")
	}
}
