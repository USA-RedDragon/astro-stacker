package server_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/server"
)

// A plan moved to the filter's shorter managed template keeps its old
// template's length in the estimate while its migration is open.
func TestSchedPlanKeepsTheOldTemplateLength(t *testing.T) {
	t.Parallel()
	db := memDB(t, "migrated")
	schedulerTables(t, db)
	exec(t, db, `insert into exposuretemplate values (3,'p','R (auto)','R',300,'m3')`,
		`update exposureplan set "exposureTemplateId" = 3 where "Id" = 40`)
	plan := func() server.SchedPlan {
		t.Helper()
		projects, _, err := server.LoadSchedProjects(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range projects {
			for _, tg := range p.Targets {
				for _, pl := range tg.Plans {
					if pl.ID == 40 {
						return pl
					}
				}
			}
		}
		t.Fatal("no plan 40")
		return server.SchedPlan{}
	}
	if pl := plan(); *pl.Exposure != 300 {
		t.Fatalf("exposure %v before any migration row, want the template's 300", *pl.Exposure)
	}
	exec(t, db, `create table ts_template_migration (plan_id integer, profile_id text, filter text, old_template_id integer, new_template_id integer, migrated_at text, reverted_at text)`,
		`insert into ts_template_migration values (40, 'p', 'R', 1, 3, '2026-10-12T03:00:00Z', null)`)
	if pl := plan(); *pl.Exposure != 600 || pl.ExposureSource != "template" || *pl.DefaultExposure != 300 {
		t.Errorf("plan %+v exposure %v, want the old template's 600", pl, *pl.Exposure)
	}
}
