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

func seedOne(t *testing.T, db *gorm.DB, vetting, class string) (int, string) {
	t.Helper()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	src := app.DiscoverySource{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.4866, Dec: 42.5082, Night: night, Vetting: vetting}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	c := app.DiscoveryCandidate{RunID: 1, Object: objAnd, Night: night, Kind: app.DiscoveryKindChange, Class: class, SourceID: &src.ID}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	list, err := summaries(context.Background(), db, nil, []int{src.ID})
	if err != nil || len(list) != 1 {
		t.Fatalf("summary %v %v", list, err)
	}
	return src.ID, hashOf(list[0])
}

func runOnce(t *testing.T, db *gorm.DB, id int, hash string, start, end time.Time, pending []string) {
	t.Helper()
	ctx := context.Background()
	if err := markRunning(ctx, db, []int{id}, start); err != nil {
		t.Fatal(err)
	}
	r := &Report{Version: Version, Suggestion: Suggestion{State: app.VetReal, Confidence: Low}, Pending: pending}
	if err := save(ctx, db, id, hash, stamp{pipeline: starPipeline}, r, nil, end); err != nil {
		t.Fatal(err)
	}
}

func pickOne(t *testing.T, db *gorm.DB, id int, at time.Time) *Due {
	t.Helper()
	due, err := Pick(context.Background(), db, at, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range due {
		if due[i].Summary.Source.ID == id {
			return &due[i]
		}
	}
	return nil
}

// checkAskHeld replays source 167 of 2026-10-20: marked real by the user,
// its candidates held back at low confidence, asked for by hand at
// 15:40:52, run 16:24:35 to 16:29:02 and left waiting on ATLAS and TESS.
// The hourly run at 19:17, the first two hours on, takes it again first
// in line for ATLAS; before, the ask was spent and the source waited six hours to be run again
// behind every open and new source in the ATLAS line.
func checkAskHeld(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	id, hash := seedOne(t, db, app.VetReal, discoveries.ClassLowConfidence)
	askedAt := time.Date(2026, 10, 20, 15, 40, 52, 0, time.UTC)
	start := time.Date(2026, 10, 20, 16, 24, 35, 0, time.UTC)
	end := time.Date(2026, 10, 20, 16, 29, 2, 0, time.UTC)
	if _, err := Request(ctx, db, id, askedAt); err != nil {
		t.Fatal(err)
	}
	runOnce(t, db, id, hash, start, end, []string{"ATLAS", "TESS"})

	if d := pickOne(t, db, id, end.Add(10*time.Minute)); d != nil {
		t.Fatalf("a run that just finished is not taken again at once: %+v", d.AskedAt)
	}
	if d := pickOne(t, db, id, time.Date(2026, 10, 20, 18, 17, 0, 0, time.UTC)); d != nil {
		t.Fatalf("the hourly runs less than %s on leave it: %+v", askPendingAfter, d.AskedAt)
	}
	next := time.Date(2026, 10, 20, 19, 17, 0, 0, time.UTC)
	d := pickOne(t, db, id, next)
	if d == nil {
		t.Fatal("the hourly run at 19:17 takes the asked source still waiting on ATLAS and TESS")
	}
	if d.AskedAt == nil || !d.AskedAt.Equal(askedAt) {
		t.Fatalf("the asked source keeps its ask, got %v want %v", d.AskedAt, askedAt)
	}
	if p := atlasPlace(*d); p != atlasAsked {
		t.Fatalf("the asked source stands at place %d in the ATLAS line, want atlasAsked", p)
	}
	if bs := batches([]Due{*d}, nil); bs[0].rank != rankAsked {
		t.Fatalf("its target goes first: rank %d", bs[0].rank)
	}

	runOnce(t, db, id, hash, next, next.Add(5*time.Minute), []string{"TESS"})
	if d := pickOne(t, db, id, next.Add(time.Hour)); d != nil {
		t.Fatalf("the next hourly run leaves it: %+v", d.AskedAt)
	}
	if d := pickOne(t, db, id, next.Add(3*time.Hour)); d == nil || d.AskedAt == nil {
		t.Fatal("the ask holds while TESS still has not answered")
	}

	answered := next.Add(3 * time.Hour)
	runOnce(t, db, id, hash, answered, answered.Add(5*time.Minute), nil)
	if d := pickOne(t, db, id, answered.Add(time.Hour)); d != nil {
		t.Fatalf("an answered ask is spent and the source waits for its weekly refresh: %+v", d.AskedAt)
	}
	if d := pickOne(t, db, id, answered.Add(refreshEvery+time.Hour)); d == nil || d.AskedAt != nil {
		t.Fatalf("the weekly refresh runs it without the ask: %+v", d)
	}
}

// checkAskLapses keeps a survey from holding an ask for good: an artifact
// the user asked for, whose ATLAS answer never comes, is run again while
// the ask holds and left alone after askHold.
func checkAskLapses(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	id, hash := seedOne(t, db, app.VetArtifact, discoveries.ClassNewVariable)
	askedAt := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	if _, err := Request(ctx, db, id, askedAt); err != nil {
		t.Fatal(err)
	}
	at := askedAt.Add(time.Minute)
	for at.Before(askedAt.Add(askHold)) {
		runOnce(t, db, id, hash, at, at.Add(5*time.Minute), []string{"ATLAS"})
		at = at.Add(askPendingAfter + time.Hour)
		if at.Before(askedAt.Add(askHold)) {
			if d := pickOne(t, db, id, at); d == nil || d.AskedAt == nil {
				t.Fatalf("the ask holds at %s", at)
			}
		}
	}
	if d := pickOne(t, db, id, askedAt.Add(askHold)); d != nil {
		t.Fatalf("past askHold an artifact the user set is left alone: %+v", d.AskedAt)
	}
	if d := pickOne(t, db, id, askedAt.Add(30*24*time.Hour)); d != nil {
		t.Fatalf("and stays left alone: %+v", d.AskedAt)
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id = ?", id).Update("state", StateFailed).Error; err != nil {
		t.Fatal(err)
	}
	if d := pickOne(t, db, id, askedAt.Add(time.Hour)); d != nil {
		t.Fatal("a failed run does not hold the ask")
	}
}

func TestAskHeldSQLite(t *testing.T) {
	t.Parallel()
	checkAskHeld(t, testdb.SQLite(t, "vetaskheld"))
}

func TestAskHeldPostgres(t *testing.T) {
	t.Parallel()
	checkAskHeld(t, testdb.Postgres(t, "vetaskheld", "vet"))
}

func TestAskLapsesSQLite(t *testing.T) {
	t.Parallel()
	checkAskLapses(t, testdb.SQLite(t, "vetasklapse"))
}

func TestAskLapsesPostgres(t *testing.T) {
	t.Parallel()
	checkAskLapses(t, testdb.Postgres(t, "vetasklapse", "vet"))
}
