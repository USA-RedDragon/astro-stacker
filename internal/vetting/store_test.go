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

const objAnd = "Andromeda"

func seedSources(t *testing.T, db *gorm.DB) (int, int, int) {
	t.Helper()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	srcs := []app.DiscoverySource{
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.889, Dec: 41.662, Night: night},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.9, Dec: 41.7, Night: night},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.95, Dec: 41.75, Night: night, Vetting: app.VetReal},
	}
	if err := db.Create(&srcs).Error; err != nil {
		t.Fatal(err)
	}
	cands := []app.DiscoveryCandidate{
		{RunID: 1, Object: objAnd, Night: night, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNewVariable, SourceID: &srcs[0].ID},
		{RunID: 1, Object: objAnd, Night: night, Kind: app.DiscoveryKindChange, Class: discoveries.ClassKnownVariable, SourceID: &srcs[1].ID},
		{RunID: 1, Object: objAnd, Night: night, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNewVariable, SourceID: &srcs[2].ID},
	}
	if err := db.Create(&cands).Error; err != nil {
		t.Fatal(err)
	}
	return srcs[0].ID, srcs[1].ID, srcs[2].ID
}

func checkPick(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	newID, knownID, vettedID := seedSources(t, db)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	ids := func(ds []Due) []int {
		out := make([]int, len(ds))
		for i, d := range ds {
			out[i] = d.Summary.Source.ID
		}
		return out
	}
	due, err := Pick(ctx, db, now, nil, nil)
	if err != nil || len(due) != 1 || due[0].Summary.Source.ID != newID {
		t.Fatalf("only the new, unvetted source is due: %v %v", ids(due), err)
	}
	if err := markRunning(ctx, db, []int{newID}, now); err != nil {
		t.Fatal(err)
	}
	if due, _ := Pick(ctx, db, now.Add(time.Hour), nil, nil); len(due) != 0 {
		t.Fatalf("a running source is not picked again: %v", ids(due))
	}
	r := &Report{Version: Version, Suggestion: Suggestion{State: app.VetReal, Confidence: High}, Pending: []string{"ATLAS"}}
	if err := save(ctx, db, newID, due0Hash(ctx, t, db), stamp{pipeline: starPipeline}, r, nil, now); err != nil {
		t.Fatal(err)
	}
	st, err := Load(ctx, db, newID)
	if err != nil || st.State != StateDone || st.Report == nil || st.Report.Suggestion.State != app.VetReal {
		t.Fatalf("saved report %+v %v", st, err)
	}
	if due, _ := Pick(ctx, db, now.Add(10*time.Minute), nil, nil); len(due) != 0 {
		t.Fatalf("a fresh report waiting on ATLAS is left alone for a while: %v", ids(due))
	}
	if due, _ := Pick(ctx, db, now.Add(2*time.Hour), nil, nil); len(due) != 1 {
		t.Fatalf("a report waiting on ATLAS is run again later: %v", ids(due))
	}
	r.Pending = nil
	if err := save(ctx, db, newID, due0Hash(ctx, t, db), stamp{pipeline: starPipeline}, r, nil, now); err != nil {
		t.Fatal(err)
	}
	if due, _ := Pick(ctx, db, now.Add(48*time.Hour), nil, nil); len(due) != 0 {
		t.Fatalf("a finished report is not run again: %v", ids(due))
	}
	night := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&app.DiscoveryCandidate{RunID: 2, Object: objAnd, Night: night, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNewVariable, SourceID: &newID}).Error; err != nil {
		t.Fatal(err)
	}
	if due, _ := Pick(ctx, db, now.Add(48*time.Hour), nil, nil); len(due) != 1 {
		t.Fatalf("a new night of the source runs it again: %v", ids(due))
	}
	if _, err := Request(ctx, db, knownID, now); err != nil {
		t.Fatal(err)
	}
	due, _ = Pick(ctx, db, now.Add(48*time.Hour), nil, []int{knownID})
	if len(due) != 1 || due[0].Summary.Source.ID != knownID {
		t.Fatalf("a requested known source runs: %v", ids(due))
	}
	if _, err := Request(ctx, db, 9999, now); err == nil {
		t.Fatal("an unknown source can't be requested")
	}
	if due, _ := Pick(ctx, db, now, nil, []int{vettedID}); len(due) != 1 {
		t.Fatalf("a source named by hand runs even when vetted: %v", ids(due))
	}
}

func due0Hash(ctx context.Context, t *testing.T, db *gorm.DB) string {
	t.Helper()
	list, err := summaries(ctx, db, nil, nil)
	if err != nil || len(list) == 0 {
		t.Fatal(err)
	}
	for _, s := range list {
		if discoveries.IsNew(s.Class) {
			return hashOf(s)
		}
	}
	return ""
}

func TestPickSQLite(t *testing.T) {
	t.Parallel()
	checkPick(t, testdb.SQLite(t, "vetpick"))
}

func TestPickPostgres(t *testing.T) {
	t.Parallel()
	checkPick(t, testdb.Postgres(t, "vetpick", "vet"))
}

func TestSlug(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Cygnis Loop Panel 2": "cygnis-loop-panel-2", "Barnard's E": "barnard-s-e", "M 15": "m-15"} {
		if got := Slug(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestTicksAndThousands(t *testing.T) {
	t.Parallel()
	if got := ticks(-0.05, 0.42, 5); len(got) != 5 || got[0] != 0 || got[4] != 0.4 {
		t.Errorf("ticks %v", got)
	}
	if thousands(1261) != "1,261" || thousands(955) != "955" || thousands(1234567) != "1,234,567" {
		t.Error("thousands")
	}
}
