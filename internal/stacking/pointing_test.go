package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calmatch"
	"github.com/USA-RedDragon/astro-stacker/internal/quality"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	subA      = "a.xisf"
	keyLightA = "LIGHT/a.xisf"
)

func pointed(id int, ra, dec, hfr float64) candidate {
	return candidate{frame: app.Frame{ID: id, MountRA: &ra, MountDec: &dec},
		score: quality.SubScore{HFR: hfr, Stars: 1000, Score: 1}}
}

// A sub whose mount pointing is off target is no longer left out by
// classify: it goes on to be stacked, flagged, and is left out only if it
// doesn't register (TestOffPointingSubsStayOnlyIfTheyRegister).
func TestOffPointingSubsAreFlagged(t *testing.T) {
	t.Parallel()
	p := &Pipeline{opts: PipelineOptions{MinScore: 0.3}}
	positions := map[string][2]float64{"Cygnis Loop Panel 2": {313.02, 29.76}}
	exp := 300.0
	ra, dec := 0.04, 0.0002 // parked
	f := app.Frame{Key: keyLightA, Object: "Cygnis Loop Panel 2", MountRA: &ra, MountDec: &dec, Exposure: &exp}
	scores := map[string]quality.SubScore{subA: {Score: 1, TargetBest: 1}}
	c, status := p.classify(f, scores, nil, positions)
	if status == app.StackStatusOffTarget || c.offBy < 30 {
		t.Errorf("parked sub: status %q, off by %.1f°", status, c.offBy)
	}
	// 2026-10-06: the mount's model 20° out, the scope on target.
	ra, dec = 296.61, 17.33
	if c, status := p.classify(f, scores, nil, positions); status == app.StackStatusOffTarget || c.offBy < 15 {
		t.Errorf("lost mount: status %q, off by %.1f°", status, c.offBy)
	}
	// Re-framed by a degree: on target.
	ra, dec = 313.02, 30.8
	if c, _ := p.classify(f, scores, nil, positions); c.offBy != 0 {
		t.Errorf("a sub a degree off is off by %.1f°", c.offBy)
	}
}

// Subs whose mount pointing is off target neither become the reference nor
// make a framing of their own, even when they are most of the subs and the
// sharpest; with nothing else they are used.
func TestOffPointingSubsAreNotTheReference(t *testing.T) {
	t.Parallel()
	// Triangulum Galaxy at RA 23.46°/Dec 30.66°; 2026-10-06's headers said
	// 6.67°/17.72°.
	cands := make([]candidate, 0, 7)
	for i := range 5 {
		c := pointed(i+1, 6.67, 17.72, 1.0)
		c.offBy = 20.5
		cands = append(cands, c)
	}
	cands = append(cands, pointed(10, 23.46, 30.66, 1.9), pointed(11, 23.47, 30.66, 1.7))
	best, ok := pickReference(mainFraming(referenceCandidates(cands)))
	if !ok || best.frame.ID != 11 {
		t.Errorf("picked %d, want 11, the sharpest pointed on target", best.frame.ID)
	}
	if best, ok := pickReference(mainFraming(referenceCandidates(cands[:5]))); !ok || best.frame.ID == 0 {
		t.Errorf("only off-pointing subs: picked %d, %v", best.frame.ID, ok)
	}
}

func TestReferenceComesFromTheMainFraming(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
			f := app.Frame{Key: string(rune('a'+i)) + "h", Type: frameTypeLight, Object: objectHorsehead, LastModified: time.Now()}
			db.Create(&f)
			status := app.StackStatusAdded
			if i < 12 {
				status = app.StackStatusRegistration
			}
			db.Create(&app.StackFrame{FrameID: f.ID, Status: status})
		}
		other := app.Frame{Key: "other", Type: frameTypeLight, Object: objectM31, LastModified: time.Now()}
		db.Create(&other)
		db.Create(&app.StackFrame{FrameID: other.ID, Status: app.StackStatusAdded})
		db.Create(&app.Stack{Object: objectHorsehead, Filter: filterRed})
		db.Create(&app.TargetReference{Object: objectHorsehead, ObjectKey: "k"})
	}
	ctx := context.Background()
	for round := 1; round <= 3; round++ {
		fill()
		replaced, err := p.maybeReReference(ctx, objectHorsehead)
		if err != nil {
			t.Fatal(err)
		}
		if want := round <= maxReReferences; replaced != want {
			t.Fatalf("round %d: replaced %v, want %v", round, replaced, want)
		}
		var left int64
		db.Model(&app.StackFrame{}).Joins("JOIN frames f ON f.id = stack_frames.frame_id").Where("f.object = ?", objectM31).Count(&left)
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
	f := app.Frame{Key: "x", Type: frameTypeLight, Object: objectHorsehead, LastModified: time.Now()}
	db.Create(&f)
	db.Create(&app.StackFrame{FrameID: f.ID, Status: app.StackStatusRegistration})
	if replaced, _ := p.maybeReReference(ctx, objectHorsehead); replaced {
		t.Error("one failure replaced the reference")
	}
}

// A sub is judged against the best its target has in that filter: under the
// moon-only panel's best it would all be low score.
func TestLowScoreIsRelativeToTheTarget(t *testing.T) {
	t.Parallel()
	p := &Pipeline{opts: PipelineOptions{MinScore: 0.3}}
	f := app.Frame{Key: keyLightA}
	for _, c := range []struct {
		score, best float64
		low         bool
	}{
		{0.2, 1, true},     // a good target's poor sub
		{0.5, 1, false},    // a good target's fine sub
		{0.1, 0.15, false}, // a moonlit-only panel's typical sub
		{0.03, 0.15, true}, // and its worst
	} {
		scores := map[string]quality.SubScore{subA: {Score: c.score, TargetBest: c.best}}
		_, status := p.classify(f, scores, nil, nil)
		if (status == app.StackStatusLowScore) != c.low {
			t.Errorf("score %v of best %v: status %q", c.score, c.best, status)
		}
	}
}

// A light with a flat and bias but no dark for its setpoint is stacked
// (and calibrated again when a dark comes); one without a flat waits.
func TestMissingDarkDoesNotHoldALightBack(t *testing.T) {
	t.Parallel()
	p := &Pipeline{opts: PipelineOptions{MinScore: 0.3}}
	night := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	exp, gain, offset, temp, bin, rot := 300.0, 0.0, 50.0, -25.0, 1.0, 132.0
	f := app.Frame{Key: keyLightA, Filter: filterRed, Exposure: &exp, Night: &night, Gain: &gain, Offset: &offset,
		SetTemp: &temp, BinX: &bin, Rotator: &rot}
	scores := map[string]quality.SubScore{subA: {Score: 1, TargetBest: 1}}
	bias := calmatch.Set{Type: "BIAS", Night: night, Gain: 0, Offset: 50, BinX: 1}
	flat := calmatch.Set{Type: "FLAT", Night: night, Filter: filterRed, Gain: 0, Offset: 50, BinX: 1, Rotator: 132}
	if c, status := p.classify(f, scores, []calmatch.Set{bias, flat}, nil); status != "" || c.cal.Dark.Set != nil {
		t.Errorf("no dark: status %q, dark %v", status, c.cal.Dark.Set)
	}
	if _, status := p.classify(f, scores, []calmatch.Set{bias}, nil); status != app.StackStatusCalibration {
		t.Errorf("no flat: status %q", status)
	}
}
