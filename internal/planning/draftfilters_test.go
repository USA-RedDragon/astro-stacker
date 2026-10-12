package planning

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const (
	filterGreen = "Green"
	filterBlue  = "Blue"
)

func lrgbTemplates(t *testing.T, db *gorm.DB) {
	t.Helper()
	execAll(t, db, `insert into exposuretemplate values
		(6,'p','Red','Red',0,10,1,0,1,30,7,85,300,0,0,1,'t6'),
		(7,'p','Green','Green',0,10,1,0,1,30,7,85,300,0,0,1,'t7'),
		(8,'p','Blue','Blue',0,10,1,0,1,30,7,85,300,0,0,1,'t8')`)
}

type sentPlan struct {
	id     int64
	name   string
	filter string
}

func sent(pl []schedcmd.NewPlan) []sentPlan {
	out := make([]sentPlan, 0, len(pl))
	for _, p := range pl {
		out = append(out, sentPlan{p.TemplateID, p.TemplateName, p.Filter})
	}
	return out
}

func samePlans(a, b []sentPlan) bool { return slices.Equal(a, b) }

func cometDraft() ProjectDraft {
	return ProjectDraft{Name: "C/2025 A1", MinimumTime: 30, MinimumAltitude: 20, Desired: 40,
		Panels:  []PanelDraft{{RAHours: 5.1, Dec: 12.3}},
		Filters: []string{filterLum, filterRed, " green ", filterBlue, "luminance"}}
}

// A comet-style LRGB project named by its filters: before the migration the
// plans use each filter's template and send no filter.
func TestDraftProjectFromFiltersBeforeMigration(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	lrgbTemplates(t, sched)
	s := load(t, sched, appDB)
	p, err := s.DraftProject(cometDraft(), seq())
	if err != nil {
		t.Fatal(err)
	}
	want := []sentPlan{{3, filterLum, ""}, {6, filterRed, ""}, {7, filterGreen, ""}, {8, filterBlue, ""}}
	if got := sent(p.Targets[0].Plans); !samePlans(got, want) {
		t.Fatalf("plans %+v, want %+v", got, want)
	}
	for _, pl := range p.Targets[0].Plans {
		if pl.Exposure != TemplateLength || pl.Desired != 40 {
			t.Errorf("plan %+v, want the system sub length and the asked count", pl)
		}
	}
	if len(p.Goals) != 4 || p.Goals[2].Filter != filterGreen {
		t.Errorf("goals %+v", p.Goals)
	}
	if b := mustJSON(t, p); strings.Contains(b, `"filter":filterRed,"exposure"`) || strings.Contains(b, `"filter":""`) {
		t.Errorf("a filter was sent before the migration: %s", b)
	}
	d := cometDraft()
	d.Filters = append(d.Filters, "Clear")
	if _, err := s.DraftProject(d, seq()); !errors.Is(err, ErrMissingTemplate) {
		t.Errorf("a filter no template uses: %v", err)
	}
	d = cometDraft()
	d.Desired = 0
	if _, err := s.DraftProject(d, seq()); !errors.Is(err, ErrNoDesired) {
		t.Errorf("filters no plan uses need a count: %v", err)
	}
	d.GoalDriven = true
	if _, err := s.DraftProject(d, seq()); err != nil {
		t.Errorf("goal-driven filters: %v", err)
	}
}

func TestDraftProjectAfterMigration(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	migrate(t, sched)
	s := load(t, sched, appDB)
	d := ProjectDraft{Name: "Pillars", MinimumTime: 30, MinimumAltitude: 30, Panels: []PanelDraft{{RAHours: 18.3, Dec: -13.8}},
		Plans:   []PlanDraft{{TemplateID: 1, Exposure: 180}, {Filter: filterO3, Exposure: 240}},
		Filters: []string{filterLum}}
	p, err := s.DraftProject(d, seq())
	if err != nil {
		t.Fatal(err)
	}
	want := []sentPlan{{11, autoHa, filterHa}, {12, autoO3, filterO3}, {13, autoLum, filterLum}}
	if got := sent(p.Targets[0].Plans); !samePlans(got, want) {
		t.Fatalf("plans %+v, want %+v", got, want)
	}
	pl := p.Targets[0].Plans
	if pl[0].Exposure != 180 || pl[1].Exposure != 240 || pl[2].Exposure != TemplateLength || pl[0].Desired != 300 {
		t.Errorf("plans %+v", pl)
	}
	if _, err := s.SettingsFor(13, 30); err != nil {
		t.Errorf("settings for a managed template: %v", err)
	}
	if err := schedValidate(p); err != nil {
		t.Errorf("the payload fails schedcmd validation: %v", err)
	}
	hoo := setNamed(t, s, "H-a (auto), O-III (auto)").ID
	p, err = s.DraftProject(ProjectDraft{Name: "Gecko", SetID: hoo, MinimumTime: 60, Panels: []PanelDraft{{RAHours: 22.5, Dec: 40.8}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	if got := sent(p.Targets[0].Plans); !samePlans(got, []sentPlan{{11, autoHa, filterHa}, {12, autoO3, filterO3}}) {
		t.Errorf("set plans %+v", got)
	}
}

func schedValidate(p *schedcmd.ProjectCreatePayload) error {
	spec, err := schedcmd.Default().Get(schedcmd.KindProjectCreate)
	if err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return spec.Validate(b)
}

func TestDraftApplySetAfterMigration(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	migrate(t, sched)
	execAll(t, sched,
		`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9')`,
		`insert into target values (30,'Pacman',1,0.88,56.6,0,9,'g30')`,
		`insert into exposureplan values (60,'p',-1,40,0,0,30,1,1,'e60')`)
	s := load(t, sched, appDB)
	hoo := setNamed(t, s, "H-a (auto), O-III (auto)").ID
	d, err := s.DraftApplySet(hoo, "add", []int{30}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload == nil || len(d.Payload.Targets) != 1 {
		t.Fatalf("%+v", d)
	}
	got := sent(d.Payload.Targets[0].Create)
	if !samePlans(got, []sentPlan{{12, autoO3, filterO3}}) {
		t.Errorf("created %+v, want only O-III: the H-a plan on the retired template already covers H-a", got)
	}
	if d.Effects[0].Effect != "adds O-III (auto)" {
		t.Errorf("effect %q", d.Effects[0].Effect)
	}
}
