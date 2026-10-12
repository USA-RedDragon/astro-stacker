package discover

import (
	"testing"
)

func migratedSnapSQL() []string {
	return []string{
		`create table ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`,
		`insert into exposuretemplate values (11,'p','H-a (auto)','H-a',100,-1,1,2,0,0,0,0,600,0,0,-1,'m11'),(12,'p','Red (auto)','Red',0,-1,1,1,0,0,0,0,300,0,0,-1,'m12'),(13,'p','Luminance (auto)','Luminance',0,-1,1,1,0,0,0,0,300,0,0,-1,'m13')`,
		`update exposureplan set "exposureTemplateId" = 11 where "exposureTemplateId" in (1, 2)`,
		`update exposureplan set "exposureTemplateId" = 12 where "exposureTemplateId" in (3, 4)`,
		`update exposureplan set "exposureTemplateId" = 13 where "exposureTemplateId" = 5`,
		`insert into ts_template_role values (11,'m11','p','H-a','managed','2026-10-12T03:00:00Z'),(12,'m12','p','Red','managed','2026-10-12T03:00:00Z'),(13,'m13','p','Luminance','managed','2026-10-12T03:00:00Z')`,
		`insert into ts_template_role select "Id", guid, 'p', filtername, 'retired', '2026-10-12T03:00:00Z' from exposuretemplate where "Id" < 11`,
	}
}

func TestTemplateForAfterMigration(t *testing.T) {
	t.Parallel()
	snap := pickSnap(t, pickSched(t, migratedSnapSQL()...))
	for canon, want := range map[string]int{"L": 13, "R": 12, "H": 11} {
		tm, basis := templateFor(snap, canon)
		if tm == nil || tm.ID != want {
			t.Errorf("%s: %+v, want the managed template %d", canon, tm, want)
			continue
		}
		if basis != "your "+tm.Filter+" filter" {
			t.Errorf("%s basis %q names a template or a plan count", canon, basis)
		}
	}
	for _, canon := range []string{"O", "G"} {
		if tm, basis := templateFor(snap, canon); tm != nil || basis != "no exposure template for "+filterLabel(canon) {
			t.Errorf("%s: %+v %q, want no template: its only ones are retired", canon, tm, basis)
		}
	}
	if set, _ := matchSet(snap, []string{"H", "O"}); set != nil {
		t.Errorf("a set of retired templates matched: %+v", set)
	}
}

func TestTemplateForBasisNamesTheFilter(t *testing.T) {
	t.Parallel()
	snap := pickSnap(t, pickSched(t))
	if tm, basis := templateFor(snap, "R"); tm == nil || tm.ID != 4 || basis != "your Red filter" {
		t.Errorf("%+v %q", tm, basis)
	}
}
