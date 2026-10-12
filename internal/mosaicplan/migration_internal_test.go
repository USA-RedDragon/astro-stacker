package mosaicplan

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/stacking"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// A panel's planned hours keep the old template's length while its plan's
// migration to the filter's shorter managed template is open.
func TestPlansKeepTheOldTemplateLength(t *testing.T) {
	t.Parallel()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`create table target ("Id" integer primary key, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, filtername text, defaultexposure real)`,
		`create table exposureplan ("Id" integer primary key, targetid integer, "exposureTemplateId" integer, exposure real, desired integer, accepted integer, enabled integer)`,
		`insert into target values (1, 'g1')`,
		`insert into exposuretemplate values (1, 'Red', 600), (2, 'Red', 300)`,
		`insert into exposureplan values (40, 1, 2, -1, 10, 0, 1)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Sched: db}
	g := stacking.MosaicGroup{Panels: []stacking.MosaicPanel{{TargetGUID: "g1"}}}
	rows, err := s.plans(context.Background(), g)
	if err != nil || len(rows) != 1 || rows[0].Default != 300 {
		t.Fatalf("%+v %v, want the template's 300 s before any migration row", rows, err)
	}
	for _, q := range []string{
		`create table ts_template_migration (plan_id integer, profile_id text, filter text, old_template_id integer, new_template_id integer, migrated_at text, reverted_at text)`,
		`insert into ts_template_migration values (40, 'p', 'Red', 1, 2, '2026-10-12T03:00:00Z', null)`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.plans(context.Background(), g)
	if err != nil || len(rows) != 1 || rows[0].Default != 600 {
		t.Fatalf("%+v %v, want the old template's 600 s", rows, err)
	}
	pf, _ := panelFilter(rows[0].Filter, app.Stack{}, false, rows, goals.Progress{}, false)
	if pf.PlannedHours != 10*600.0/3600 {
		t.Errorf("planned hours %v, want 10 subs at 600 s", pf.PlannedHours)
	}
}
