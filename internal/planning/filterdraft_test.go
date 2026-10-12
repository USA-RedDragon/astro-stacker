package planning

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

func validApplySet(t *testing.T, p *schedcmd.ApplySetPayload) {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := schedcmd.Default().Get(schedcmd.KindApplySet)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(b); err != nil {
		t.Fatalf("the payload fails exposureplan.applyset validation: %v", err)
	}
}

func TestDraftFiltersGroupsPlansByFilter(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	lrgbTemplates(t, sched)
	execAll(t, sched, `insert into exposuretemplate values (9,'p','R 600','Red',0,10,1,0,1,60,7,85,600,0,0,1,'t9')`,
		`insert into exposureplan values (60,'p',-1,50,5,5,12,6,1,'e60'),(61,'p',-1,50,9,9,12,9,1,'e61')`)
	s := load(t, sched, appDB)
	d, err := s.DraftFilters([]int{12, 20, 13}, []string{filterHa, " luminance", filterRed, filterHa}, seq())
	if err != nil {
		t.Fatal(err)
	}
	if d.Payload == nil || d.Payload.SetID != FilterSetID || d.Payload.SetName != FilterSetName || d.Payload.Mode != "replace" || len(d.Payload.Targets) != 3 {
		t.Fatalf("payload %+v", d.Payload)
	}
	validApplySet(t, d.Payload)
	cyg := d.Payload.Targets[0]
	if got := sent(cyg.Create); !samePlans(got, []sentPlan{{3, filterLum, ""}}) || cyg.Create[0].Desired != 1 || cyg.Create[0].Exposure != TemplateLength {
		t.Errorf("Cygnus creates %+v", cyg.Create)
	}
	if len(cyg.Disable) != 1 || cyg.Disable[0].ID != 41 || len(cyg.Enable) != 0 {
		t.Errorf("Cygnus %+v: Red and R 600 both count as Red, O-III goes", cyg)
	}
	garlic := d.Payload.Targets[1]
	if got := sent(garlic.Create); !samePlans(got, []sentPlan{{6, filterRed, ""}}) || garlic.Create[0].Desired != 50 {
		t.Errorf("Garlic creates %+v", garlic.Create)
	}
	if len(garlic.Enable) != 1 || garlic.Enable[0].ID != 52 || len(garlic.Disable) != 1 || garlic.Disable[0].ID != 51 {
		t.Errorf("Garlic %+v", garlic)
	}
	want := []string{
		"adds Luminance, turns off O-III (its 200 kept subs stay)",
		"adds Red, turns on Luminance, turns off O-III (its 47 kept subs stay)",
		"adds Luminance, Red, turns off O-III (its 10 kept subs stay)",
	}
	for i, e := range d.Effects {
		if e.Effect != want[i] || !e.Changes {
			t.Errorf("effect %d %q, want %q", i, e.Effect, want[i])
		}
	}
	if d.Effects[0].Current != "H-a, O-III, Red" {
		t.Errorf("current %q names templates, want filters", d.Effects[0].Current)
	}
}

func TestDraftFiltersNothingToChangeAndErrors(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	execAll(t, sched, `update exposureplan set accepted = 1 where "Id" = 43`)
	s := load(t, sched, appDB)
	d, err := s.DraftFilters([]int{13}, []string{filterHa, filterO3}, seq())
	if err != nil || d.Payload != nil || d.Effects[0].Effect != "Already has these filters; nothing changes" {
		t.Fatalf("%+v %v", d, err)
	}
	d, _ = s.DraftFilters([]int{13}, []string{filterHa}, seq())
	if d.Effects[0].Effect != "turns off O-III (its 1 kept sub stays)" {
		t.Errorf("effect %q", d.Effects[0].Effect)
	}
	if d, err := s.DraftFilters([]int{13}, []string{filterHa, "Clear"}, seq()); !errors.Is(err, ErrMissingTemplate) || len(d.Missing) != 1 || d.Missing[0] != "Clear" {
		t.Errorf("unknown filter: %+v %v", d, err)
	}
	if _, err := s.DraftFilters([]int{13}, []string{" "}, seq()); !errors.Is(err, ErrNoFilters) {
		t.Errorf("no filters: %v", err)
	}
	if _, err := s.DraftFilters(nil, []string{filterHa}, seq()); !errors.Is(err, ErrNoTargets) {
		t.Errorf("no targets: %v", err)
	}
	if _, err := s.DraftFilters([]int{99}, []string{filterHa}, seq()); err == nil {
		t.Error("an unknown target drafted")
	}
}

func TestDraftFiltersAfterMigration(t *testing.T) {
	t.Parallel()
	sched, appDB := testDBs(t)
	migrate(t, sched)
	s := load(t, sched, appDB)
	d, err := s.DraftFilters([]int{12}, []string{filterHa, filterLum}, seq())
	if err != nil {
		t.Fatal(err)
	}
	validApplySet(t, d.Payload)
	if got := sent(d.Payload.Targets[0].Create); !samePlans(got, []sentPlan{{13, autoLum, filterLum}}) {
		t.Errorf("creates %+v, want the managed Luminance template with its filter", got)
	}
	if d.Effects[0].Effect != "adds Luminance, turns off O-III (its 200 kept subs stay)" {
		t.Errorf("effect %q", d.Effects[0].Effect)
	}
}
