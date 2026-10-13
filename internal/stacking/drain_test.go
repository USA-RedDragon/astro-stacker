package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
	"github.com/USA-RedDragon/astro-stacker/internal/wake"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// With nothing in hand, a drained pipeline's status reporting stops at
// once instead of holding Run until the drain's limit.
func TestDrainStopsStatusReporting(t *testing.T) {
	t.Parallel()
	p := &Pipeline{drain: make(chan struct{}), working: map[string]*events.Worker{}, Events: events.NewBroker()}
	done := make(chan struct{})
	go func() {
		p.reportStatus(context.Background())
		close(done)
	}()
	p.Drain()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("status reporting kept running after a drain")
	}
}

// A calibrated target that fell back to stars is solved again once there
// is a new way to solve it, and only then.
func TestRetrySolve(t *testing.T) {
	t.Parallel()
	tl := app.Frame{Key: "Telescope.live/Crescent/SPA-3-CCD_2021_x_cal.fits"}
	nina := app.Frame{Key: "Crescent/LIGHT/2025_H-a_0001.xisf"}
	for _, c := range []struct {
		name string
		tr   app.TargetReference
		ref  app.Frame
		want bool
	}{
		{"chosen before distortion", app.TargetReference{}, tl, true},
		{"stars under an older revision", app.TargetReference{Registration: registrationStars, SolveRevision: 1}, tl, true},
		{"stars under this revision", app.TargetReference{Registration: registrationStars, SolveRevision: solveRevision}, tl, false},
		{"solved", app.TargetReference{Registration: registrationDistortion, SolveRevision: 1}, tl, false},
		{"our own subs", app.TargetReference{Registration: registrationStars}, nina, false},
	} {
		if got := retrySolve(c.tr, c.ref); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The verdict and mosaic loops wait on kicks between runs; a drain ends
// that wait at once.
func TestDrainStopsKickedLoops(t *testing.T) {
	t.Parallel()
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", DefaultPipelineOptions())
	done := make(chan struct{})
	go func() {
		p.runVerdicts(context.Background(), VerdictOptions{})
		close(done)
	}()
	p.KickVerdicts()
	p.KickMosaics()
	p.Drain()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the verdict loop kept waiting after a drain")
	}
	ctx, cancel := p.untilDrained(context.Background())
	defer cancel()
	if ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the wait context outlived the drain")
		}
	}
}

func TestVerdictBudgetIsPerHour(t *testing.T) {
	t.Parallel()
	p := &Pipeline{}
	now := time.Now()
	if got := p.verdictBudget(0, now); got != 200 {
		t.Fatalf("default budget %d", got)
	}
	p.verdictNews = []time.Time{now.Add(-2 * time.Hour), now.Add(-30 * time.Minute), now.Add(-time.Minute)}
	if got := p.verdictBudget(3, now); got != 1 || len(p.verdictNews) != 2 {
		t.Fatalf("budget %d, kept %d", got, len(p.verdictNews))
	}
	if got := p.verdictBudget(1, now); got != 0 {
		t.Fatalf("an overspent hour leaves nothing: %d", got)
	}
}

// A comet master starts a minute after its star-aligned master settles,
// not after MosaicQuiet: a master updated moments ago asks for another pass.
func TestCometMasterWaitsOnlyForTheMasterToSettle(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Stack{}); err != nil {
		t.Fatal(err)
	}
	key := "masters/161P.xisf"
	if err := db.Create(&app.Stack{Object: hartleyName, Filter: "L", Subs: 5, MasterKey: &key, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(nil, "", "", db, nil, siril.Runner{}, "", DefaultPipelineOptions())
	p.mosaicKick = &trigger.Debouncer{}
	runs := make(chan struct{}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.mosaicKick.Run(ctx, 0, func(context.Context, []string) { runs <- struct{}{} })
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	<-runs
	if err := p.cometsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runs:
	case <-time.After(5 * time.Second):
		t.Fatal("a comet master whose master just changed was left for the backstop")
	}
	var s app.Stack
	if err := db.First(&s).Error; err != nil {
		t.Fatal(err)
	}
	if !p.cometRebuildDue(s.ID) {
		t.Fatal("a master never built since the start is due")
	}
	p.cometBuiltNow(s.ID)
	if p.cometRebuildDue(s.ID) {
		t.Fatal("a comet master built moments ago waits CometRebuildGap")
	}
	if err := db.Model(&s).UpdateColumn("updated_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := p.cometsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runs:
	case <-time.After(5 * time.Second):
		t.Fatal("a comet master inside its rebuild gap asks for another pass")
	}
}

// A moving target's master update keeps the workers off it until the next
// body night pass, so its night is measured between batches.
func TestBodyMasterUpdateReservesTheTargetForItsNight(t *testing.T) {
	t.Parallel()
	p := NewPipeline(nil, "", "", nil, nil, siril.Runner{}, "", DefaultPipelineOptions())
	p.bodyKick = &trigger.Debouncer{}
	p.bodyObjects = map[string]bool{hartleyName: true}
	master := events.Event{Type: events.TypeMaster, Object: hartleyName, Filter: "L"}
	p.onBodyEvent(master)
	if len(p.waiting) != 0 {
		t.Fatal("with no body night loop running nothing is reserved")
	}
	p.setBodyLoop(true)
	p.onBodyEvent(events.Event{Type: events.TypeMaster, Object: objectM31, Filter: "L"})
	p.onBodyEvent(events.Event{Type: events.TypeSubs, Object: hartleyName, Filter: "L"})
	p.onBodyEvent(master)
	p.onBodyEvent(master)
	if p.waiting[hartleyName] != 1 || len(p.waiting) != 1 {
		t.Fatalf("one reservation for the moving target only: %v", p.waiting)
	}
	held := p.takeReservations()
	p.onBodyEvent(master)
	if p.waiting[hartleyName] != 2 {
		t.Fatalf("an update during a pass reserves for the next one: %v", p.waiting)
	}
	p.Wake = &wake.Signal{}
	woke := p.Wake.C()
	p.dropReservations(held)
	select {
	case <-woke:
	default:
		t.Fatal("handing a target back does not wake the workers")
	}
	p.dropReservations(p.takeReservations())
	if len(p.waiting) != 0 || len(p.reserved) != 0 {
		t.Fatalf("reservations left: %v %v", p.waiting, p.reserved)
	}
	p.setBodyLoop(false)
	p.onBodyEvent(master)
	if len(p.waiting) != 0 {
		t.Fatal("a stopped body night loop reserves nothing")
	}
}
