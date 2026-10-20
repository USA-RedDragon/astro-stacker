package vetting

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

// checkLowHeld keeps the report of a source held back at low confidence
// after its vet current, as for the 49 such sources of 2026-10-20 whose
// reports came from older pipelines, while the low confidence sources
// never vetted stay unvetted.
func checkLowHeld(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	stale, staleHash := seedOne(t, db, "", discoveries.ClassLowConfidence)
	never, _ := seedOne(t, db, "", discoveries.ClassLowConfidence)
	stuck, _ := seedOne(t, db, "", discoveries.ClassLowConfidence)
	now := time.Date(2026, 10, 20, 17, 17, 0, 0, time.UTC)
	runOnce(t, db, stale, staleHash, now.Add(-9*24*time.Hour), now.Add(-9*24*time.Hour), nil)
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", stale).
		Updates(map[string]any{"pipeline": "star:12", "input_hash": "older-pipeline"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := markRunning(ctx, db, []int{stuck}, now.Add(-4*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx, db, []int{stuck}, now.Add(-4*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if d := pickOne(t, db, never, now); d != nil {
		t.Fatal("a low confidence source never vetted stays unvetted")
	}
	d := pickOne(t, db, stale, now)
	if d == nil {
		t.Fatal("a low confidence source with an older pipeline's report is vetted again")
	}
	if p := atlasPlace(*d); p != atlasSettled {
		t.Fatalf("it stands at place %d in the ATLAS line, want atlasSettled", p)
	}
	if pickOne(t, db, stuck, now) == nil {
		t.Fatal("a low confidence source a stopped run handed back is vetted")
	}
	runOnce(t, db, stale, staleHash, now, now.Add(5*time.Minute), nil)
	if d := pickOne(t, db, stale, now.Add(30*24*time.Hour)); d != nil {
		t.Fatal("a current report of a low confidence source is left alone")
	}
}

func TestLowHeldSQLite(t *testing.T) {
	t.Parallel()
	checkLowHeld(t, testdb.SQLite(t, "vetlowheld"))
}

func TestLowHeldPostgres(t *testing.T) {
	t.Parallel()
	checkLowHeld(t, testdb.Postgres(t, "vetlowheld", "vet"))
}
