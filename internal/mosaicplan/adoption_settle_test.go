package mosaicplan_test

import (
	"context"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const byPerson = "web"

func adoptionSvc(t *testing.T) (*mosaicplan.Service, *gorm.DB, *gorm.DB) {
	t.Helper()
	appDB := appFixture(t)
	addUnmatchedSubs(t, appDB)
	sched := schedFixture(t)
	svc := mosaicplan.New(appDB, sched)
	svc.Rig = mosaics.Rig{WidthDeg: 3.32, HeightDeg: 2.22, ScaleArcsec: 1.915}
	return svc, appDB, sched
}

func runAdoption(ctx context.Context, t *testing.T, svc *mosaicplan.Service) (mosaicplan.Report, map[string]mosaicplan.Adoption) {
	t.Helper()
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	return rep, adoptionsBySubject(t, rep.Items)
}

func countLinks(t *testing.T, db *gorm.DB, object string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&app.FrameTarget{}).Where("object = ?", object).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunAdoptionReopensAutoRowsTheEvidenceNoLongerSettles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, appDB, sched := adoptionSvc(t)
	_, by := runAdoption(ctx, t, svc)
	if a := by["subs "+markarian]; a.Status != app.AdoptionAuto || countLinks(t, appDB, markarian) != 2 {
		t.Fatalf("markarian subs %+v", a)
	}
	if err := sched.Exec(`INSERT INTO target VALUES (30, 'Elsewhere', 1, ?, 13.45, 0, 3, 'near')`, (186.75+0.3/0.9743700647852352)/15).Error; err != nil {
		t.Fatal(err)
	}
	_, by = runAdoption(ctx, t, svc)
	a := by["subs "+markarian]
	if a.Frames == nil || a.Frames.Within != 2 || a.Frames.TargetGUID != "m1" {
		t.Fatalf("fixture %+v %+v", a, a.Frames)
	}
	if a.Status != app.AdoptionProposed || a.DecidedAt != nil || countLinks(t, appDB, markarian) != 0 {
		t.Errorf("automatic attachment kept with two targets in range %+v links %d", a, countLinks(t, appDB, markarian))
	}
	third, _ := runAdoption(ctx, t, svc)
	if third.Auto != 0 || third.Changed != 0 {
		t.Errorf("third run %+v", third)
	}
}

func TestRunAdoptionReopensRowsForProjectsBackInTS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, sched := adoptionSvc(t)
	runAdoption(ctx, t, svc)
	if err := sched.Exec(`UPDATE target SET projectid = 99 WHERE projectid = 3`).Error; err != nil {
		t.Fatal(err)
	}
	_, by := runAdoption(ctx, t, svc)
	if by["Rosette"].Status != app.AdoptionAuto {
		t.Fatalf("removed project not closed %+v", by["Rosette"])
	}
	if err := sched.Exec(`UPDATE target SET projectid = 3 WHERE projectid = 99`).Error; err != nil {
		t.Fatal(err)
	}
	rep, by := runAdoption(ctx, t, svc)
	if a := by["Rosette"]; a.Status != app.AdoptionProposed || a.DecidedAt != nil {
		t.Errorf("project back in TS stays closed %+v report %+v", a, rep)
	}
}

func TestRunAdoptionLeavesAPersonsChangedRowForThePerson(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, appDB, sched := adoptionSvc(t)
	_, by := runAdoption(ctx, t, svc)
	sh := by["subs Sh2-1"]
	if _, err := mosaicstore.SetStatus(ctx, appDB, sh.ID, app.AdoptionProposed, app.AdoptionAccepted, byPerson, svc.Rig); err != nil {
		t.Fatal(err)
	}
	if n := countLinks(t, appDB, "Sh2-1"); n != 1 {
		t.Fatalf("%d links after accepting", n)
	}
	for _, q := range []string{`UPDATE target SET dec = -30 WHERE guid = 'r1'`, `INSERT INTO target VALUES (31, 'Sh2-1 West', 1, 16.4, -24.2, 0, 3, 'sw')`} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	rep, by := runAdoption(ctx, t, svc)
	a := by["subs Sh2-1"]
	if a.Frames == nil || a.Frames.TargetGUID != "sw" || a.Frames.Within != 1 || !a.Frames.NameMatch {
		t.Fatalf("fixture %+v", a.Frames)
	}
	if a.Status != app.AdoptionProposed || a.DecidedBy != byPerson || a.DecidedAt == nil {
		t.Errorf("a person's decision was replaced automatically %+v report %+v", a, rep)
	}
	if n := countLinks(t, appDB, "Sh2-1"); n != 0 {
		t.Errorf("%d links to the old target left on a row that no longer proposes it", n)
	}
	again, _ := runAdoption(ctx, t, svc)
	if again.Auto != 0 || again.Changed != 0 {
		t.Errorf("second run %+v", again)
	}
}

func TestRunAdoptionSkipsWhenTSHasNoProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, sched := adoptionSvc(t)
	_, by := runAdoption(ctx, t, svc)
	before := by["Rosette"]
	if err := sched.Exec(`DELETE FROM target`).Error; err != nil {
		t.Fatal(err)
	}
	rep, err := svc.RunAdoption(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Auto != 0 || len(rep.Items) != 0 {
		t.Fatalf("an empty Target Scheduler read closed rows %+v", rep)
	}
	list, err := svc.Adoptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.ID == before.ID && a.Status != before.Status {
			t.Fatalf("row changed after an empty read %+v", a)
		}
	}
}
