package planning

import (
	"encoding/json"
	"strings"
	"testing"

	"gorm.io/gorm"
)

const (
	autoHa     = "H-a (auto)"
	autoLum    = "Luminance (auto)"
	changedAt  = "2026-10-12T03:00:00Z"
	profileP   = "p"
	filterRed  = "Red"
	autoO3     = "O-III (auto)"
	roleInsert = `insert into ts_template_role values (?,?,?,?,?,?)`
)

func execAll(t *testing.T, db *gorm.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func setRole(t *testing.T, db *gorm.DB, id int, filter, role string) {
	t.Helper()
	if err := db.Exec(roleInsert, id, nil, profileP, filter, role, changedAt).Error; err != nil {
		t.Fatal(err)
	}
}

// migrate does what the plugin's templates.migrate does to the test data:
// managed templates per filter in use, plans moved onto them, the rest
// retired.
func migrate(t *testing.T, db *gorm.DB) {
	t.Helper()
	execAll(t, db, roleTable,
		`insert into exposuretemplate values
			(11,'p','H-a (auto)','H-a',100,-1,1,2,0,0,0,0,600,0,0,-1,'m11'),
			(12,'p','O-III (auto)','O-III',100,-1,1,2,0,0,0,0,600,0,0,-1,'m12'),
			(13,'p','Luminance (auto)','Luminance',0,-1,1,1,0,0,0,0,300,0,0,-1,'m13')`,
		`update exposureplan set "exposureTemplateId" = 11 where "exposureTemplateId" = 1`,
		`update exposureplan set "exposureTemplateId" = 12 where "exposureTemplateId" = 2`,
		`update exposureplan set "exposureTemplateId" = 13 where "exposureTemplateId" = 3`)
	for id, f := range map[int]string{11: filterHa, 12: filterO3, 13: filterLum} {
		setRole(t, db, id, f, RoleManaged)
	}
	for id, f := range map[int]string{1: filterHa, 2: filterO3, 3: filterLum} {
		setRole(t, db, id, f, RoleRetired)
	}
}

func TestResolveFilterBeforeMigration(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	execAll(t, sched,
		`insert into exposuretemplate values (4,'p','Lum 100','Luminance',100,10,1,0,1,30,7,85,300,0,0,1,'t4'),(5,'p','HA Low','H-a',0,10,1,2,1,30,7,85,120,0,0,1,'t5'),(9,'p','R 100','Red',100,10,1,0,1,30,7,85,300,0,0,1,'t9')`,
		`insert into exposureplan values (70,'p',-1,10,0,0,20,4,1,'e70'),(71,'p',-1,10,0,0,20,4,1,'e71'),(72,'p',-1,10,0,0,12,5,1,'e72'),(73,'p',-1,10,0,0,13,5,1,'e73'),(74,'p',-1,10,0,0,20,5,1,'e74'),(75,'p',-1,10,0,0,20,5,1,'e75'),(76,'p',-1,10,0,0,20,5,1,'e76')`)
	s := load(t, sched, appDB)
	if tm, _ := s.ResolveFilter(profileP, filterLum); tm.ID != 3 {
		t.Errorf("Luminance resolved to %d, want 3: the gain-0 template beats the more used gain-100 one", tm.ID)
	}
	if tm, _ := s.ResolveFilter(profileP, "h-a"); tm.ID != 1 {
		t.Errorf("H-a resolved to %d, want 1: HA Low has more plans but gain 0", tm.ID)
	}
	if tm, ok := s.ResolveFilter(profileP, filterRed); !ok || tm.ID != 9 {
		t.Errorf("Red resolved to %d %v, want the only Red template whatever its gain", tm.ID, ok)
	}
	if _, ok := s.ResolveFilter(profileP, "Clear"); ok {
		t.Error("a filter no template uses resolved")
	}
	if _, ok := s.ResolveFilter("other", filterLum); ok {
		t.Error("another profile's template resolved")
	}
	if s.Migrated(profileP) {
		t.Error("migrated before the migration")
	}

	execAll(t, sched, roleTable)
	setRole(t, sched, 3, filterLum, RoleRetired)
	s = load(t, sched, appDB)
	if tm, _ := s.ResolveFilter(profileP, filterLum); tm.ID != 4 {
		t.Errorf("Luminance resolved to %d, want 4: a retired template is passed over", tm.ID)
	}
	setRole(t, sched, 4, filterLum, RoleRetired)
	s = load(t, sched, appDB)
	if tm, ok := s.ResolveFilter(profileP, filterLum); !ok || tm.ID != 4 {
		t.Errorf("Luminance resolved to %d %v, want the most used retired one when all are retired", tm.ID, ok)
	}
	if !s.Migrated(profileP) || s.Migrated("other") {
		t.Error("a retired row did not mark only its profile migrated")
	}
}

func TestSnapshotTemplatesAndFilters(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s := load(t, sched, appDB)
	want := []Filter{
		{Name: filterHa, Narrowband: true, Gain: 100, TemplateID: 1},
		{Name: filterO3, Narrowband: true, Gain: 100, TemplateID: 2},
		{Name: filterLum, Gain: 0, TemplateID: 3},
	}
	if b, w := mustJSON(t, s.Filters), mustJSON(t, want); b != w {
		t.Errorf("filters before the migration %s, want %s", b, w)
	}

	migrate(t, sched)
	s = load(t, sched, appDB)
	if len(s.Templates) != 3 {
		t.Fatalf("templates %+v, want the 3 retired ones and no managed one", s.Templates)
	}
	for _, tm := range s.Templates {
		if !tm.Retired || tm.Managed || strings.HasSuffix(tm.Name, "(auto)") {
			t.Errorf("template %+v", tm)
		}
	}
	want = []Filter{
		{Name: filterHa, Narrowband: true, Gain: 100, TemplateID: 11, Managed: true},
		{Name: filterO3, Narrowband: true, Gain: 100, TemplateID: 12, Managed: true},
		{Name: filterLum, Gain: 0, TemplateID: 13, Managed: true},
	}
	if b, w := mustJSON(t, s.Filters), mustJSON(t, want); b != w {
		t.Errorf("filters after the migration %s, want %s", b, w)
	}
	raw := mustJSON(t, s)
	if !strings.Contains(raw, `"retired":true`) || !strings.Contains(raw, `"filters":[{"name":"H-a","narrowband":true,"gain":100,"templateId":11,"managed":true}`) || strings.Contains(raw, `"Managed"`) {
		t.Errorf("snapshot json %s", raw)
	}
	if !s.Migrated(profileP) {
		t.Error("not migrated after the migration")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// After a revert the retired rows are gone and only managed templates still
// used by plans keep their rows: the profile is not migrated, so filters
// resolve to the old templates and no filter is sent.
func TestManagedRowsWithoutRetiredAreNotMigrated(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	execAll(t, sched, roleTable,
		`insert into exposuretemplate values (13,'p','Luminance (auto)','Luminance',0,-1,1,1,0,0,0,0,300,0,0,-1,'m13'),(14,'p','S-II (auto)','S-II',100,-1,1,2,0,0,0,0,600,0,0,-1,'m14')`,
		`insert into exposureplan values (70,'p',-1,10,0,0,20,13,1,'e70'),(71,'p',-1,10,0,0,20,14,1,'e71')`)
	setRole(t, sched, 13, filterLum, RoleManaged)
	setRole(t, sched, 14, "S-II", RoleManaged)
	s := load(t, sched, appDB)
	if s.Migrated(profileP) {
		t.Fatal("managed rows alone marked the profile migrated")
	}
	if tm, _ := s.ResolveFilter(profileP, filterLum); tm.ID != 3 {
		t.Errorf("Luminance resolved to %d, want the old template 3", tm.ID)
	}
	if tm, ok := s.ResolveFilter(profileP, "S-II"); !ok || tm.ID != 14 {
		t.Errorf("S-II resolved to %d %v, want the managed template when it is the only one", tm.ID, ok)
	}
	tm, _ := s.ResolveFilter(profileP, filterLum)
	if f := s.PlanFilter(tm); f != "" {
		t.Errorf("a filter %q was sent before the migration", f)
	}
}
