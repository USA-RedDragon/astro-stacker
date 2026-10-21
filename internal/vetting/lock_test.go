package vetting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

func TestLockPostgres(t *testing.T) {
	t.Parallel()
	db := testdb.Postgres(t, "vetlock", "vet")
	ctx := context.Background()
	unlock, err := Lock(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(ctx, db); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second run took the lock: %v", err)
	}
	unlock()
	again, err := Lock(ctx, db)
	if err != nil {
		t.Fatalf("the lock was not given back: %v", err)
	}
	again()
}

func checkRequeue(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	newID, _, _ := seedSources(t, db)
	started := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := markRunning(ctx, db, []int{newID}, started); err != nil {
		t.Fatal(err)
	}
	now := started.Add(time.Hour)
	if due, _ := Pick(ctx, db, now, nil, nil); len(due) != 0 {
		t.Fatalf("a running source is left to its run: %d due", len(due))
	}
	stale, err := requeueStale(ctx, db, now)
	if err != nil || len(stale) != 1 || !stale[newID] {
		t.Fatalf("stale %v %v", stale, err)
	}
	st, err := Load(ctx, db, newID)
	if err != nil || st.State != StateQueued || st.Error != stoppedNote {
		t.Fatalf("requeued %+v %v", st, err)
	}
	due, err := Pick(ctx, db, now, nil, nil)
	if err != nil || len(due) != 1 || due[0].Summary.Source.ID != newID {
		t.Fatalf("a source a stopped run left is due again: %v", err)
	}
	if err := markRunning(ctx, db, []int{newID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := save(ctx, db, newID, due[0].Hash, stamp{pipeline: starPipeline}, &Report{Version: Version}, nil, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, _ := Pick(ctx, db, now.Add(3*time.Minute), nil, nil); len(due) != 0 {
		t.Fatalf("a finished retry is not run again: %d due", len(due))
	}
	if stale, _ := requeueStale(ctx, db, now.Add(3*time.Minute)); len(stale) != 0 {
		t.Fatalf("nothing is running: %v", stale)
	}
	again := now.Add(time.Hour)
	if err := markRunning(ctx, db, []int{newID}, again); err != nil {
		t.Fatal(err)
	}
	if stale, _ := requeueStale(ctx, db, again.Add(time.Minute)); !stale[newID] {
		t.Fatalf("a new stop is retried: %v", stale)
	}
	if err := markRunning(ctx, db, []int{newID}, again.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if stale, _ := requeueStale(ctx, db, again.Add(3*time.Minute)); len(stale) != 0 {
		t.Fatalf("a retry that stopped too is not retried at once: %v", stale)
	}
	st, err = Load(ctx, db, newID)
	if err != nil || st.State != StateFailed || st.Error != stoppedAgainNote {
		t.Fatalf("stopped twice %+v %v", st, err)
	}
	if due, _ := Pick(ctx, db, again.Add(time.Hour), nil, nil); len(due) != 0 {
		t.Fatalf("a source that stopped twice waits: %d due", len(due))
	}
	if due, _ := Pick(ctx, db, again.Add(7*time.Hour), nil, nil); len(due) != 1 {
		t.Fatalf("a source that stopped twice is tried after the wait: %d due", len(due))
	}
}

func TestRequeueSQLite(t *testing.T) {
	t.Parallel()
	checkRequeue(t, testdb.SQLite(t, "vetrequeue"))
}

func TestRequeuePostgres(t *testing.T) {
	t.Parallel()
	checkRequeue(t, testdb.Postgres(t, "vetrequeue", "vet"))
}

func TestBatchesOrder(t *testing.T) {
	t.Parallel()
	at := func(h int) *time.Time {
		v := time.Date(2026, 10, 16, h, 0, 0, 0, time.UTC)
		return &v
	}
	d := func(id int, object string, asked *time.Time) Due {
		return Due{Summary: discoverysource.Summary{Source: app.DiscoverySource{ID: id, Object: object}}, AskedAt: asked}
	}
	dues := []Due{
		d(1, "Abell 85", nil), d(2, "Abell 85", nil), d(3, "Pelican Nebula Panel 2", nil), d(4, "Pelican Nebula Panel 2", at(5)),
		d(5, "IC 1396 Panel 1", at(4)), d(6, barnardsE, at(6)), d(7, "IC 1396 Panel 1", nil),
	}
	got := batches(dues, map[int]bool{6: true})
	want := []struct {
		object string
		ids    []int
	}{
		{"IC 1396 Panel 1", []int{5, 7}}, {"Pelican Nebula Panel 2", []int{4, 3}},
		{"Abell 85", []int{1, 2}}, {barnardsE, []int{6}},
	}
	if len(got) != len(want) {
		t.Fatalf("%d batches, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].object != w.object || len(got[i].dues) != len(w.ids) {
			t.Fatalf("batch %d is %s with %d sources, want %s %v", i, got[i].object, len(got[i].dues), w.object, w.ids)
		}
		for j, id := range w.ids {
			if got[i].dues[j].Summary.Source.ID != id {
				t.Errorf("batch %d source %d is %d, want %d", i, j, got[i].dues[j].Summary.Source.ID, id)
			}
		}
	}
}

func checkAskedAndRelease(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	newID, _, _ := seedSources(t, db)
	now := time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	if _, err := Request(ctx, db, newID, now); err != nil {
		t.Fatal(err)
	}
	due, err := Pick(ctx, db, now.Add(time.Minute), nil, nil)
	if err != nil || len(due) != 1 || due[0].AskedAt == nil || !due[0].AskedAt.Equal(now) {
		t.Fatalf("a source asked for by hand says when: %+v %v", due, err)
	}
	if err := markRunning(ctx, db, []int{newID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx, db, []int{newID}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, err := Load(ctx, db, newID)
	if err != nil || st.State != StateQueued {
		t.Fatalf("released %+v %v", st, err)
	}
	due, err = Pick(ctx, db, now.Add(3*time.Minute), nil, nil)
	if err != nil || len(due) != 1 || due[0].AskedAt == nil || !due[0].AskedAt.Equal(now) {
		t.Fatalf("a released source asked for by hand keeps its place: %+v %v", due, err)
	}
}

func TestAskedAndReleaseSQLite(t *testing.T) {
	t.Parallel()
	checkAskedAndRelease(t, testdb.SQLite(t, "vetasked"))
}

func TestAskedAndReleasePostgres(t *testing.T) {
	t.Parallel()
	checkAskedAndRelease(t, testdb.Postgres(t, "vetasked", "vet"))
}
