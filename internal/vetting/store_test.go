package vetting

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
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
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.95, Dec: 41.75, Night: night, Vetting: app.VetArtifact},
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
	r := &Report{Version: Version, Suggestion: Suggestion{State: app.VetReal, Confidence: High}, Pending: []string{lc.ATLAS}}
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

func checkRefreshPick(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	srcs := []app.DiscoverySource{
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.1, Dec: 41.1, Night: night},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.2, Dec: 41.2, Night: night, Vetting: app.VetReal},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.3, Dec: 41.3, Night: night, Vetting: app.VetArtifact},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.4, Dec: 41.4, Night: night, Vetting: app.VetKnown},
		{Object: objAnd, Kind: app.DiscoveryKindMover, RA: 10.5, Dec: 41.5, Night: night, Vetting: app.VetReal},
	}
	if err := db.Create(&srcs).Error; err != nil {
		t.Fatal(err)
	}
	for i := range srcs {
		c := app.DiscoveryCandidate{RunID: 1, Object: objAnd, Night: night, Kind: srcs[i].Kind, Class: discoveries.ClassNewVariable, SourceID: &srcs[i].ID}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	unvetted, marked, artifact, known, mover := srcs[0].ID, srcs[1].ID, srcs[2].ID, srcs[3].ID, srcs[4].ID
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	pick := func(at time.Time) map[int]bool {
		t.Helper()
		due, err := Pick(ctx, db, at, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int]bool{}
		for _, d := range due {
			out[d.Summary.Source.ID] = true
		}
		return out
	}
	hashes := map[int]string{}
	list, err := summaries(ctx, db, nil, []int{unvetted, marked, artifact, known, mover})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		hashes[s.Source.ID] = hashOf(s)
	}
	if got := pick(now); len(got) != 2 || !got[unvetted] || !got[marked] {
		t.Fatalf("with no reports the unvetted and the user-real sources are due, not artifact, known or a moving object: %v", got)
	}
	saveAt := func(id int, at time.Time, hash, pipeline string, pending []string) {
		t.Helper()
		if err := markRunning(ctx, db, []int{id}, at); err != nil {
			t.Fatal(err)
		}
		r := &Report{Version: Version, Suggestion: Suggestion{State: app.VetUnsure, Confidence: Low}, Pending: pending}
		if err := save(ctx, db, id, hash, stamp{pipeline: pipeline}, r, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int{unvetted, marked, artifact, known, mover} {
		saveAt(id, now, hashes[id], starPipeline, nil)
	}
	if got := pick(now.Add(6 * 24 * time.Hour)); len(got) != 0 {
		t.Fatalf("nothing is due six days after a run: %v", got)
	}
	if got := pick(now.Add(refreshEvery)); len(got) != 1 || !got[marked] {
		t.Fatalf("seven days on only the user-real source is due: %v", got)
	}
	if err := db.Model(&app.DiscoveryVet{}).Where("source_id IN ?", []int{unvetted, marked, artifact, known, mover}).
		Updates(map[string]any{"pipeline": "star:7", "input_hash": "older-pipeline"}).Error; err != nil {
		t.Fatal(err)
	}
	if got := pick(now.Add(time.Hour)); len(got) != 2 || !got[unvetted] || !got[marked] {
		t.Fatalf("after a pipeline bump the unvetted and user-real sources are due, not artifact, known or a moving object: %v", got)
	}
	saveAt(marked, now, hashes[marked], starPipeline, []string{lc.ATLAS})
	if got := pick(now.Add(2 * time.Hour)); got[marked] {
		t.Fatalf("a user-real source waiting on ATLAS waits %s: %v", realPendingAfter, got)
	}
	if got := pick(now.Add(realPendingAfter + time.Minute)); !got[marked] {
		t.Fatalf("a user-real source waiting on ATLAS is run again after %s: %v", realPendingAfter, got)
	}
	if _, err := Request(ctx, db, artifact, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := pick(now.Add(2 * time.Hour)); !got[artifact] || got[known] {
		t.Fatalf("an artifact asked for by hand runs; known does not: %v", got)
	}
	var vetted []app.DiscoverySource
	if err := db.Order("id").Find(&vetted, []int{unvetted, marked, artifact, known, mover}).Error; err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"", app.VetReal, app.VetArtifact, app.VetKnown, app.VetReal} {
		if vetted[i].Vetting != want {
			t.Fatalf("source %d vetting %q, want the user's %q", vetted[i].ID, vetted[i].Vetting, want)
		}
	}
}

func TestRefreshPickSQLite(t *testing.T) {
	t.Parallel()
	checkRefreshPick(t, testdb.SQLite(t, "vetrefresh"))
}

func TestRefreshPickPostgres(t *testing.T) {
	t.Parallel()
	checkRefreshPick(t, testdb.Postgres(t, "vetrefresh", "vet"))
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
	for in, want := range map[string]string{"Cygnis Loop Panel 2": "cygnis-loop-panel-2", barnardsE: "barnard-s-e", "M 15": "m-15"} {
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

func TestWatchedRefreshPickSQLite(t *testing.T) {
	t.Parallel()
	db := testdb.SQLite(t, "vetwatch")
	ctx := context.Background()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}, &app.DiscoveryWatch{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	srcs := []app.DiscoverySource{
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.1, Dec: 41.1, Night: night},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.2, Dec: 41.2, Night: night},
		{Object: objAnd, Kind: app.DiscoveryKindChange, RA: 10.3, Dec: 41.3, Night: night},
	}
	if err := db.Create(&srcs).Error; err != nil {
		t.Fatal(err)
	}
	for i := range srcs {
		c := app.DiscoveryCandidate{RunID: 1, Object: objAnd, Night: night, Kind: srcs[i].Kind, Class: discoveries.ClassNewVariable, SourceID: &srcs[i].ID}
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	plain, watched, ended := srcs[0].ID, srcs[1].ID, srcs[2].ID
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	list, err := summaries(ctx, db, nil, []int{plain, watched, ended})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if err := markRunning(ctx, db, []int{s.Source.ID}, now); err != nil {
			t.Fatal(err)
		}
		r := &Report{Version: Version, Suggestion: Suggestion{State: app.VetReal, Confidence: Low}}
		if err := save(ctx, db, s.Source.ID, hashOf(s), stamp{pipeline: starPipeline}, r, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	at := now
	if err := db.Create(&[]app.DiscoveryWatch{{SourceID: watched, StartedAt: now, LastFadeAt: now},
		{SourceID: ended, StartedAt: now, LastFadeAt: now, EndedAt: &at, EndReason: app.WatchEndFades}}).Error; err != nil {
		t.Fatal(err)
	}
	pick := func(at time.Time) map[int]bool {
		t.Helper()
		due, err := Pick(ctx, db, at, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int]bool{}
		for _, d := range due {
			out[d.Summary.Source.ID] = true
			if d.Summary.Source.ID == watched && !refreshed(d.Summary) {
				t.Error("a watched source should be vetted with fresh survey data")
			}
		}
		return out
	}
	if got := pick(now.Add(6 * 24 * time.Hour)); len(got) != 0 {
		t.Fatalf("nothing is due six days after a run: %v", got)
	}
	if got := pick(now.Add(refreshEvery)); len(got) != 1 || !got[watched] {
		t.Fatalf("seven days on only the watched source is due: %v", got)
	}
}
