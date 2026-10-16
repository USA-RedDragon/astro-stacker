package vetting

import (
	"context"
	"errors"
	"testing"
	"time"

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
	if err := save(ctx, db, newID, due[0].Hash, &Report{Version: Version}, nil, now.Add(2*time.Minute)); err != nil {
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

