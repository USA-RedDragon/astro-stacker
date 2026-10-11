package goalmeasure

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

func TestPassKeepsRegionMeasurementsWhenGoalsCannotBeRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	e.addGoalTables(t)
	if err := e.sched.Exec(`INSERT INTO ts_goal VALUES ('g-synth', 'H-a', 0, 15, NULL, 1, '[{"x":0.1,"y":0.1},{"x":0.5,"y":0.1},{"x":0.5,"y":0.6},{"x":0.1,"y":0.6}]', '2026-10-01 00:00:00')`).Error; err != nil {
		t.Fatal(err)
	}
	e.addStack(t, testObject, testFilter, 16)
	tr := app.TargetReference{Object: testObject, FrameID: 1, ObjectKey: "region-ref"}
	w := wcsJSON(t, testWCS())
	tr.WCS = &w
	if err := e.db.Create(&tr).Error; err != nil {
		t.Fatal(err)
	}
	r := New(e.db, e.sched, e.objects, e.fetcher, Options{MaxSubs: 200, Publish: goals.PublishOff})
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	before := e.measurement(t, testObject, testFilter)
	if before.RegionHash == "" {
		t.Fatalf("no region measurement %+v", before)
	}
	fail := func(tx *gorm.DB) {
		if tx.Statement.Table == goals.GoalTable {
			tx.Statement.SQL.Reset()
			tx.Statement.SQL.WriteString("SELECT 1 FROM injected_failure")
			tx.Statement.Vars = nil
		}
	}
	if err := e.sched.Callback().Query().Before("gorm:query").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if err := e.sched.Callback().Row().Before("gorm:row").Register("test:fail", fail); err != nil {
		t.Fatal(err)
	}
	if err := r.Pass(ctx); err == nil {
		t.Error("a pass without goals succeeded")
	}
	if after := e.measurement(t, testObject, testFilter); after.RegionHash != before.RegionHash || after.TargetGUID != before.TargetGUID {
		t.Fatalf("region measurement overwritten: hash %q -> %q, target %q -> %q", before.RegionHash, after.RegionHash, before.TargetGUID, after.TargetGUID)
	}
}
