package skyevents

import (
	"context"
	"testing"
)

const roleTableSQL = `create table ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`

func TestPickFilterFallsBackToTheMostUsedBroadband(t *testing.T) {
	t.Parallel()
	snap := planSnap(t, planSched(t, `delete from exposureplan where "exposureTemplateId" in (3, 4)`, `delete from exposuretemplate where filtername = 'Luminance'`))
	f, ok := pickFilter(snap)
	if !ok || f != "Red" {
		t.Fatalf("picked %q %v, want Red", f, ok)
	}
	if tm, _ := pickTemplate(snap); tm.ID != 5 {
		t.Fatalf("template %+v, want R 600, the Red template most plans use", tm)
	}
	none := planSnap(t, planSched(t, `delete from exposuretemplate where gain = 0`))
	if _, ok := pickTemplate(none); ok {
		t.Fatal("picked a template with only narrowband ones")
	}
}

func TestEventDraftAfterMigrationSendsTheFilter(t *testing.T) {
	t.Parallel()
	db := planSched(t, roleTableSQL,
		`insert into exposuretemplate values (13,'p','Luminance (auto)','Luminance',0,-1,1,1,0,0,0,0,300,0,0,-1,'m13'),(14,'p','Red (auto)','Red',0,-1,1,1,0,0,0,0,300,0,0,-1,'m14'),(15,'p','H-a (auto)','H-a',100,-1,1,2,0,0,0,0,600,0,0,-1,'m15')`,
		`update exposureplan set "exposureTemplateId" = 13 where "exposureTemplateId" in (3, 4)`,
		`update exposureplan set "exposureTemplateId" = 14 where "exposureTemplateId" in (2, 5)`,
		`update exposureplan set "exposureTemplateId" = 15 where "exposureTemplateId" = 1`,
		`insert into ts_template_role values (13,'m13','p','Luminance','managed','2026-10-12T03:00:00Z'),(14,'m14','p','Red','managed','2026-10-12T03:00:00Z'),(15,'m15','p','H-a','managed','2026-10-12T03:00:00Z')`,
		`insert into ts_template_role values (1,'t1','p','H-a','retired','2026-10-12T03:00:00Z'),(2,'t2','p','Red','retired','2026-10-12T03:00:00Z'),(3,'t3','p','Luminance','retired','2026-10-12T03:00:00Z'),(4,'t4','p','Luminance','retired','2026-10-12T03:00:00Z'),(5,'t5','p','Red','retired','2026-10-12T03:00:00Z')`)
	snap := planSnap(t, db)
	p, err := (&Service{}).Draft(context.Background(), snap, draftEvent(), PriorityNormal, ids())
	if err != nil {
		t.Fatal(err)
	}
	pl := p.Targets[0].Plans
	if len(pl) != 1 || pl[0].TemplateID != 13 || pl[0].TemplateName != "Luminance (auto)" || pl[0].Filter != "Luminance" || pl[0].Exposure != 300 {
		t.Fatalf("plans %+v, want the managed Luminance template with its filter", pl)
	}
}
