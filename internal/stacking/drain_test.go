package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/siril"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
