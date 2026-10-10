package schedcmd_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

type fakeSnapshot struct {
	state schedcmd.SchedulerState
	ok    bool
	plans map[int64][]schedcmd.PlanState
}

func (f fakeSnapshot) SchedulerState() (schedcmd.SchedulerState, bool) { return f.state, f.ok }

func (f fakeSnapshot) TargetPlans(_ context.Context, id int64) ([]schedcmd.PlanState, error) {
	return f.plans[id], nil
}

func before(t *testing.T, r schedcmd.Record) any {
	t.Helper()
	if len(r.Diffs) == 0 {
		t.Fatalf("no diffs: %+v", r)
	}
	var v any
	if err := json.Unmarshal(r.Diffs[0].Before, &v); err != nil && len(r.Diffs[0].Before) > 0 {
		t.Fatal(err)
	}
	return v
}

func TestBeforeValuesComeFromLiveState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newService(t)
	skip := json.RawMessage(`{"scope":"target","target_id":29,"target_name":"Triangulum","minutes":30}`)
	r, err := s.Submit(ctx, schedcmd.KindSkip, skip, author)
	if err != nil || before(t, r) != nil {
		t.Fatalf("no snapshot must leave before empty: %+v %v", r.Diffs, err)
	}
	paused, until := true, time.Date(2026, 10, 10, 5, 0, 0, 0, time.UTC)
	on, off := true, false
	s.Snapshot = fakeSnapshot{ok: true,
		state: schedcmd.SchedulerState{State: "paused", Paused: &paused, Skips: []schedcmd.SkipState{{Scope: "target", TargetID: 29, Until: &until}}},
		plans: map[int64][]schedcmd.PlanState{29: {{Template: "H-a", Enabled: &on}, {Template: "Luminance", Enabled: &off}}},
	}
	r, _ = s.Submit(ctx, schedcmd.KindUnskip, skip, author)
	if got := before(t, r); got != "Skipped until 2026-10-10 05:00 UTC" {
		t.Fatalf("unskip before %v", got)
	}
	r, _ = s.Submit(ctx, schedcmd.KindResume, nil, author)
	if got := before(t, r); got != "Paused" {
		t.Fatalf("resume before %v", got)
	}
	r, _ = s.Submit(ctx, schedcmd.KindApplySet, json.RawMessage(samples()[schedcmd.KindApplySet]), author)
	if got := before(t, r); got != "H-a; off: Luminance" {
		t.Fatalf("applyset before %v", got)
	}
	s.Snapshot = fakeSnapshot{}
	r, _ = s.Submit(ctx, schedcmd.KindPause, nil, author)
	if got := before(t, r); got != nil {
		t.Fatalf("offline pause before %v", got)
	}
}
