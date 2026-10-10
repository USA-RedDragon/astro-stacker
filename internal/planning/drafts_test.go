package planning

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
)

func seq() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("id-%d", n) }
}

func TestDraftApplySet(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DraftApplySet("lrgb", "add", []int{20}, 0, seq()); !errors.Is(err, ErrUnknownSet) {
		t.Fatalf("an unknown set should fail: %v", err)
	}
	hoo := setNamed(t, s, setHOO).ID
	d, err := s.DraftApplySet(hoo, "add", []int{20, 12}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload != nil || d.Effects[0].Changes {
		t.Fatalf("garlic already has HOO: %+v", d)
	}
	for _, q := range []string{
		`insert into exposuretemplate values (4,'p','S-II','S-II',100,10,1,2,1,45,7,85,600,0,0,1,'t4')`,
		`insert into project values (9,'p','Pacman',null,1,1,30,20,0,0,1,'pg9')`,
		`insert into target values (30,'Pacman',1,0.88,56.6,0,9,'g30')`,
		`insert into exposureplan values (60,'p',600,40,0,0,30,4,1,'e60'),(61,'p',600,40,0,0,30,1,1,'e61')`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	s, _ = Load(context.Background(), sched, appDB, Inputs{})
	sho := setNamed(t, s, "S-II, H-a")
	if sho.Projects != 1 || sho.Items[0].Desired != 40 {
		t.Fatalf("%+v", sho)
	}
	d, err = s.DraftApplySet(sho.ID, "replace", []int{20}, 120, seq())
	if err != nil {
		t.Fatal(err)
	}
	at := d.Payload.Targets[0]
	if len(at.Create) != 1 || at.Create[0].TemplateName != "S-II" || at.Create[0].Desired != 120 || at.Create[0].GUID != "id-1" {
		t.Fatalf("%+v", at)
	}
	if len(at.Disable) != 1 || at.Disable[0].TemplateName != filterO3 || len(at.Enable) != 0 {
		t.Fatalf("O-III should be turned off: %+v", at)
	}
	d, _ = s.DraftApplySet(sho.ID, "add", []int{20}, 0, seq())
	if d.Payload.Targets[0].Create[0].Desired != 40 {
		t.Fatalf("desired should come from the set: %+v", d.Payload.Targets[0])
	}
	d, _ = s.DraftApplySet(hoo, "replace", []int{12}, 0, seq())
	if d.Payload != nil {
		t.Fatalf("%+v", d.Payload)
	}
	sched.Exec(`update exposureplan set enabled = 0 where "Id" = 41`)
	s, _ = Load(context.Background(), sched, appDB, Inputs{})
	d, _ = s.DraftApplySet(hoo, "add", []int{12}, 0, seq())
	if d.Payload == nil || len(d.Payload.Targets[0].Enable) != 1 || d.Effects[0].Effect != "turns on O-III" {
		t.Fatalf("%+v", d)
	}
}

func TestDraftProjectMosaic(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	hoo := setNamed(t, s, setHOO).ID
	p, err := s.DraftProject(ProjectDraft{Name: "Gecko Nebula", Catalog: "LBN 437", Priority: "High", SetID: hoo, MinimumTime: 60, MinimumAltitude: 15,
		Goal: GoalDraft{Kind: "snr", SNR: 12}, Panels: []PanelDraft{{RAHours: 22.5, Dec: 40.8}, {RAHours: 22.7, Dec: 40.8}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Project.IsMosaic || p.Project.Priority != 2 || p.Targets[1].Name != "Gecko Nebula Panel 2" || len(p.Targets[0].Plans) != 2 || p.Targets[0].Plans[0].Desired != 300 {
		t.Fatalf("%+v", p)
	}
	if len(p.Goals) != 4 || p.Goals[0].Setting.SNRGoal != 12 || p.RuleWeights["Panel Deficit"] != 75 || p.RuleWeights["Mosaic Completion"] != 0 || p.RuleWeights["Rarity"] != 20 {
		t.Fatalf("%+v", p.Goals)
	}
	if _, err := s.DraftProject(ProjectDraft{Name: "garlic nebula", SetID: hoo, MinimumTime: 60, Panels: []PanelDraft{{}}}, seq()); err == nil {
		t.Fatal("duplicate name should fail")
	}
	if _, err := s.DraftProject(ProjectDraft{Name: "No time", SetID: hoo, Panels: []PanelDraft{{}}}, seq()); err == nil {
		t.Fatal("a missing minimum time should fail")
	}
	d, err := s.DraftProject(ProjectDraft{Name: "Deep", SetID: hoo, MinimumTime: 30, Goal: GoalDraft{Kind: "depth"}, Panels: []PanelDraft{{RAHours: 1, Dec: 2}}}, seq())
	if err != nil || d.Goals[0].Setting.DepthGoal == nil || *d.Goals[0].Setting.DepthGoal != 25.5 || d.Project.IsMosaic || d.Project.MinimumAltitude != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestDraftProjectFromPickedPlans(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	if err := sched.Exec(`insert into exposuretemplate values (4,'p','S-II','S-II',100,10,1,2,1,45,7,85,600,0,0,1,'t4')`).Error; err != nil {
		t.Fatal(err)
	}
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	base := ProjectDraft{Name: "Eagle", MinimumTime: 30, MinimumAltitude: 30, Panels: []PanelDraft{{RAHours: 18.3, Dec: -13.8}}}
	d := base
	d.Plans = []PlanDraft{{TemplateID: 1, Exposure: 180}, {TemplateID: 2, Exposure: 240}}
	p, err := s.DraftProject(d, seq())
	if err != nil {
		t.Fatal(err)
	}
	pl := p.Targets[0].Plans
	if len(pl) != 2 || pl[0].TemplateName != filterHa || pl[0].Exposure != 180 || pl[0].Desired != 300 || pl[1].Exposure != 240 || len(p.Goals) != 2 {
		t.Fatalf("%+v %+v", pl, p.Goals)
	}
	d.Plans = []PlanDraft{{TemplateID: 4, Exposure: 300}}
	if _, err := s.DraftProject(d, seq()); !errors.Is(err, ErrNoDesired) {
		t.Fatalf("a template no plan uses needs a desired count: %v", err)
	}
	d.GoalDriven = true
	if p, err := s.DraftProject(d, seq()); err != nil || p.Targets[0].Plans[0].Desired != goals.UnmeasurableSubLimit {
		t.Fatalf("goal-driven count: %+v %v", p, err)
	}
	d.Plans = []PlanDraft{{TemplateID: 99, Exposure: 300}}
	if _, err := s.DraftProject(d, seq()); !errors.Is(err, ErrMissingTemplate) {
		t.Fatalf("unknown template: %v", err)
	}
	d.Plans = []PlanDraft{{TemplateID: 1}}
	if _, err := s.DraftProject(d, seq()); !errors.Is(err, ErrNoExposure) {
		t.Fatalf("no sub length: %v", err)
	}
	if _, err := s.DraftProject(base, seq()); !errors.Is(err, ErrUnknownSet) {
		t.Fatalf("neither a set nor plans: %v", err)
	}
}
