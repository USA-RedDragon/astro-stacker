package planning

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
	if _, err := s.DraftApplySet("lrgb", "add", []int{20}, 0, seq()); err == nil || !strings.Contains(err.Error(), "Red") {
		t.Fatalf("missing templates should fail: %v", err)
	}
	d, err := s.DraftApplySet(setIDHoo, "add", []int{20, 12}, 0, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload != nil || d.Effects[0].Changes {
		t.Fatalf("garlic already has HOO: %+v", d)
	}
	sched.Exec(`insert into exposuretemplate values (4,'p','S-II','S-II',100,10,1,2,1,45,7,85,600,0,0,1,'t4')`)
	s, _ = Load(context.Background(), sched, appDB, Inputs{})
	d, err = s.DraftApplySet("sho", "replace", []int{20}, 120, seq())
	if err != nil {
		t.Fatal(err)
	}
	at := d.Payload.Targets[0]
	if len(at.Create) != 1 || at.Create[0].TemplateName != "S-II" || at.Create[0].Desired != 120 || at.Create[0].GUID != "id-1" {
		t.Fatalf("%+v", at)
	}
	if len(at.Disable) != 0 || len(at.Enable) != 0 {
		t.Fatalf("luminance is already off: %+v", at)
	}
	d, _ = s.DraftApplySet(setIDHoo, "replace", []int{12}, 0, seq())
	if d.Payload != nil {
		t.Fatalf("%+v", d.Payload)
	}
	sched.Exec(`update exposureplan set enabled = 0 where "Id" = 41`)
	s, _ = Load(context.Background(), sched, appDB, Inputs{})
	d, _ = s.DraftApplySet(setIDHoo, "add", []int{12}, 0, seq())
	if d.Payload == nil || len(d.Payload.Targets[0].Enable) != 1 || d.Effects[0].Effect != "turns on O-III" {
		t.Fatalf("%+v", d)
	}
}

func TestDraftProjectMosaic(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	s, _ := Load(context.Background(), sched, appDB, Inputs{})
	p, err := s.DraftProject(ProjectDraft{Name: "Gecko Nebula", Catalog: "LBN 437", Priority: "High", SetID: setIDHoo,
		Goal: GoalDraft{Kind: "snr", SNR: 12}, Panels: []PanelDraft{{RAHours: 22.5, Dec: 40.8}, {RAHours: 22.7, Dec: 40.8}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Project.IsMosaic || p.Project.Priority != 2 || p.Targets[1].Name != "Gecko Nebula Panel 2" || len(p.Targets[0].Plans) != 2 {
		t.Fatalf("%+v", p)
	}
	if len(p.Goals) != 4 || p.Goals[0].Setting.SNRGoal != 12 || p.RuleWeights["Panel Deficit"] != 75 || p.RuleWeights["Mosaic Completion"] != 0 || p.RuleWeights["Rarity"] != 20 {
		t.Fatalf("%+v", p.Goals)
	}
	if _, err := s.DraftProject(ProjectDraft{Name: "garlic nebula", SetID: setIDHoo, Panels: []PanelDraft{{}}}, seq()); err == nil {
		t.Fatal("duplicate name should fail")
	}
	d, err := s.DraftProject(ProjectDraft{Name: "Deep", SetID: setIDHoo, Goal: GoalDraft{Kind: "depth"}, Panels: []PanelDraft{{RAHours: 1, Dec: 2}}}, seq())
	if err != nil || d.Goals[0].Setting.DepthGoal == nil || *d.Goals[0].Setting.DepthGoal != 25.5 || d.Project.IsMosaic {
		t.Fatalf("%+v %v", d, err)
	}
}
