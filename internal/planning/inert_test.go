package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gorm.io/gorm"
)

const inertGolden = "testdata/inert_drafts.golden.json"

var retiredField = regexp.MustCompile(`,"retired":false`)

func inertOutputs(t *testing.T, sched, appDB *gorm.DB) []byte {
	t.Helper()
	for _, q := range []string{
		`insert into exposuretemplate values (4,'p','S-II','S-II',100,10,1,2,1,45,7,85,600,0,0,1,'t4'),(5,'p','Red','Red',0,10,1,0,1,30,7,85,300,0,0,1,'t5')`,
		`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9')`,
		`insert into target values (30,'Pacman',1,0.88,56.6,0,9,'g30')`,
		`insert into exposureplan values (60,'p',600,40,0,0,30,4,1,'e60'),(61,'p',600,40,0,0,30,1,1,'e61'),(62,'p',-1,90,0,0,30,5,1,'e62')`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	add := func(name string, v any, err error) {
		if err != nil {
			out[name] = "error: " + err.Error()
			return
		}
		out[name] = v
	}
	hoo := setNamed(t, s, setHOO).ID
	sho := setNamed(t, s, "S-II, H-a, Red").ID
	d, err := s.DraftProject(ProjectDraft{Name: "Gecko Nebula", Catalog: "LBN 437", Priority: "High", SetID: hoo, MinimumTime: 60, MinimumAltitude: 15,
		Goal: GoalDraft{Kind: "snr", SNR: 12}, Panels: []PanelDraft{{RAHours: 22.5, Dec: 40.8}, {RAHours: 22.7, Dec: 40.8}}}, seq())
	add("project_set", d, err)
	d, err = s.DraftProject(ProjectDraft{Name: "Eagle", MinimumTime: 30, MinimumAltitude: 30, Panels: []PanelDraft{{RAHours: 18.3, Dec: -13.8}},
		Plans: []PlanDraft{{TemplateID: 1, Exposure: 180}, {TemplateID: 5}, {TemplateID: 3, Exposure: 120}}, GoalDriven: true}, seq())
	add("project_plans", d, err)
	a, err := s.DraftApplySet(sho, "replace", []int{20, 12}, 0, seq())
	add("applyset_replace", a, err)
	a, err = s.DraftApplySet(hoo, "add", []int{30}, 25, seq())
	add("applyset_add", a, err)
	st, err := s.SettingsFor(5, 30)
	add("settings_red", st, err)
	tm, err := json.Marshal(s.Templates)
	if err != nil {
		t.Fatal(err)
	}
	out["templates"] = json.RawMessage(retiredField.ReplaceAll(tm, nil))
	out["sets"] = s.Sets
	out["defaults"] = s.Defaults
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// With no ts_template_role table, or one without managed or retired rows,
// every payload and list is what it was before template roles existed.
func TestTemplateRolesInertBeforeMigration(t *testing.T) {
	t.Parallel()
	variants := map[string][]string{
		"no table":    nil,
		"empty table": {roleTable},
	}
	for name, setup := range variants {
		sched, appDB := testDBs(t)
		for _, q := range setup {
			if err := sched.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
		got := inertOutputs(t, sched, appDB)
		if os.Getenv("ASTRO_GOLDEN_WRITE") != "" {
			if err := os.MkdirAll(filepath.Dir(inertGolden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inertGolden, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(inertGolden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: drafts differ from before template roles:\n%s", name, got)
		}
	}
}

const roleTable = `create table ts_template_role (template_id integer not null primary key, template_guid text, profile_id text not null, filter text not null, role text not null, changed_at text not null)`
