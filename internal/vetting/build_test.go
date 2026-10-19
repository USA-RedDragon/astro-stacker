package vetting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"gorm.io/gorm"
)

const buildOld = "old"

func checkBuilds(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	newID, _, _ := seedSources(t, db)
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if err := Announce(ctx, db, buildOld, t0); err != nil {
		t.Fatal(err)
	}
	due, err := Pick(ctx, db, t0, nil, nil)
	if err != nil || len(due) != 1 {
		t.Fatalf("pick %v %v", due, err)
	}
	if err := markRunning(ctx, db, []int{newID}, t0); err != nil {
		t.Fatal(err)
	}
	if err := Announce(ctx, db, "new", t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ann, ok, err := announced(ctx, db)
	if err != nil || !ok || ann.Build != "new" {
		t.Fatalf("announced %+v %v %v", ann, ok, err)
	}
	if !superseded(ann, buildOld, t0) {
		t.Fatal("a run of the old build that started before the new build was announced is superseded")
	}
	if superseded(ann, "new", t0.Add(11*time.Minute)) {
		t.Fatal("the announced build's own run is not superseded")
	}
	if err := save(ctx, db, newID, due[0].Hash, stamp{pipeline: starPipeline, build: buildOld}, &Report{Version: Version}, nil, t0.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, err := Load(ctx, db, newID)
	if err != nil || !st.Stale || st.Report.Build != buildOld || st.Report.Pipeline != starPipeline {
		t.Fatalf("a report the old build wrote after the new one was announced reads stale: %+v %v", st, err)
	}
	if due, _ := Pick(ctx, db, t0.Add(21*time.Minute), nil, nil); len(due) != 1 {
		t.Fatalf("and is vetted again: %v", due)
	}
	if err := save(ctx, db, newID, due[0].Hash, stamp{pipeline: starPipeline, build: "new"}, &Report{Version: Version}, nil, t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if st, _ := Load(ctx, db, newID); st.Stale {
		t.Fatal("the new build's report is current")
	}
	if due, _ := Pick(ctx, db, t0.Add(31*time.Minute), nil, nil); len(due) != 0 {
		t.Fatalf("the new build's report is not vetted again: %v", due)
	}
	checkNewerPipeline(t, db, newID, due[0].Hash, t0)
}

func checkNewerPipeline(t *testing.T, db *gorm.DB, newID int, hash string, t0 time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := Announce(ctx, db, buildOld, t0.Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ann, _, _ := announced(ctx, db); ann.Build != buildOld {
		t.Fatal("a build with the same pipelines may announce itself again, as on a rollback")
	}
	next := "star:99"
	err := save(ctx, db, newID, hash, stamp{pipeline: next, build: "future"}, &Report{Version: Version}, nil, t0.Add(50*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	err = save(ctx, db, newID, hash, stamp{pipeline: starPipeline, build: buildOld}, &Report{Version: Version}, nil, t0.Add(60*time.Minute))
	if !errors.Is(err, errNewerVet) {
		t.Fatalf("an older pipeline does not overwrite a newer one's report: %v", err)
	}
	var row app.DiscoveryVet
	if err := db.Where("source_id = ?", newID).First(&row).Error; err != nil || row.Pipeline != next || row.Build != "future" {
		t.Fatalf("the newer report stays: %+v %v", row, err)
	}
	if due, _ := Pick(ctx, db, t0.Add(61*time.Minute), nil, nil); len(due) != 0 {
		t.Fatalf("older code does not vet again a newer pipeline's report: %v", due)
	}
	if err := db.Model(&app.VetBuild{}).Where("id = ?", buildRowID).Update("pipelines", "mover:99,transient:99,star:99").Error; err != nil {
		t.Fatal(err)
	}
	if err := Announce(ctx, db, buildOld, t0.Add(70*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ann, _, _ := announced(ctx, db)
	if ann.Pipelines != "mover:99,transient:99,star:99" || !superseded(ann, buildOld, t0.Add(80*time.Minute)) {
		t.Fatalf("older code neither announces over newer pipelines nor keeps writing: %+v", ann)
	}
}

func TestBuildsSQLite(t *testing.T) {
	t.Parallel()
	checkBuilds(t, testdb.SQLite(t, "vetbuild"))
}

func TestBuildsPostgres(t *testing.T) {
	t.Parallel()
	checkBuilds(t, testdb.Postgres(t, "vetbuild", "vet"))
}

func TestPipelineOrder(t *testing.T) {
	t.Parallel()
	if !newerPipeline("mover:10", "mover:9") || newerPipeline("mover:9", "mover:10") || newerPipeline("star:9", "mover:1") {
		t.Fatal("pipelines compare by number within one kind")
	}
	row := app.DiscoveryVet{Pipeline: "star:1", Report: "{}"}
	if !staleRow(app.VetBuild{}, false, row, starPipeline) || !staleRow(app.VetBuild{}, false, row, "") {
		t.Fatal("a report from an older pipeline is stale")
	}
	if staleRow(app.VetBuild{}, false, app.DiscoveryVet{Pipeline: starPipeline}, starPipeline) {
		t.Fatal("a report from the current pipeline is not")
	}
}
