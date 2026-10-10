package schedcmd_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

var planningSamples = map[schedcmd.Kind]string{
	schedcmd.KindGoalEdit:          `{"project_id":5,"project_name":"Cygnis Loop","goals":[{"target_id":12,"target_guid":"g12","target_name":"Cygnis Loop Panel 1","filter":"H-a","before":null,"after":{"kind":0,"snr_goal":10,"plateau_stop":true}},{"target_id":13,"target_guid":"g13","target_name":"Cygnis Loop Panel 2","filter":"H-a","before":{"kind":0,"snr_goal":10,"plateau_stop":true},"after":{"kind":0,"snr_goal":12,"plateau_stop":true,"region":"[{\"x\":0.1,\"y\":0.1},{\"x\":0.5,\"y\":0.1},{\"x\":0.3,\"y\":0.6}]"}}]}`,
	schedcmd.KindRuleWeightEdit:    `{"project_id":5,"project_name":"Cygnis Loop","changes":[{"rule":"Novelty","before":null,"after":10},{"rule":"Target Switch Penalty","before":67,"after":40}]}`,
	"exposuretemplate.edit":        `{"id":3,"name":"H-a","changes":[{"field":"moonavoidanceseparation","before":45,"after":60}]}`,
	schedcmd.KindTemplateBatchEdit: `{"items":[{"id":3,"name":"H-a","changes":[{"field":"moonavoidanceseparation","before":45,"after":60}]},{"id":4,"name":"O-III","changes":[{"field":"moonavoidanceseparation","before":45,"after":60}]}]}`,
	schedcmd.KindProjectBatchEdit:  `{"items":[{"id":5,"name":"Cygnis Loop","changes":[{"field":"priority","before":0,"after":2}]},{"id":7,"name":"California","changes":[{"field":"priority","before":0,"after":2}]}]}`,
	schedcmd.KindPlanBatchEdit:     `{"items":[{"id":40,"name":"H-a","parent":"Cygnis Loop Panel 2","changes":[{"field":"desired","before":300,"after":400}]},{"id":41,"name":"O-III","parent":"Cygnis Loop Panel 2","changes":[{"field":"enabled","before":true,"after":false}]}]}`,
	schedcmd.KindTemplateClone:     `{"source_id":3,"source_name":"H-a","guid":"t-new","name":"H-a 300","defaultexposure":300,"gain":100}`,
	schedcmd.KindTemplateDelete:    `{"guid":"t-new","name":"H-a 300"}`,
	schedcmd.KindApplySet:          `{"set_id":"hoo","set_name":"HOO","mode":"replace","targets":[{"target_id":29,"target_guid":"g29","target_name":"Triangulum","project":"Triangulum","create":[{"guid":"p1","template_id":3,"template_name":"H-a","exposure":600,"desired":300}],"disable":[{"id":90,"guid":"p90","template_name":"Luminance"}]}]}`,
	schedcmd.KindProjectCreate:     `{"project":{"guid":"pg","name":"Gecko Nebula","priority":1,"state":1,"minimumtime":60,"minimumaltitude":15},"targets":[{"guid":"tg","name":"Gecko Nebula","ra_hours":22.53,"dec":40.83,"rotation":0,"plans":[{"guid":"pp","template_id":3,"template_name":"H-a","exposure":600,"desired":300}]}],"goals":[{"target_guid":"tg","filter":"H-a","setting":{"kind":0,"snr_goal":10,"plateau_stop":true}}],"catalog":"LBN 437"}`,
}

func spec(t *testing.T, k schedcmd.Kind) schedcmd.Spec {
	t.Helper()
	s, err := schedcmd.Default().Get(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGoalEditDescribeAndInverse(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindGoalEdit)
	d, err := s.Describe(json.RawMessage(planningSamples[schedcmd.KindGoalEdit]))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Cygnis Loop · goals" || len(d.Diffs) != 2 || !strings.Contains(d.Note, "2 targets") {
		t.Fatalf("%+v", d)
	}
	var after string
	_ = json.Unmarshal(d.Diffs[1].After, &after)
	if after != "SNR 12 · 3-point region" {
		t.Fatalf("after %q", after)
	}
	k, inv, err := s.Inverse(json.RawMessage(planningSamples[schedcmd.KindGoalEdit]))
	if err != nil || k != schedcmd.KindGoalEdit {
		t.Fatal(err)
	}
	var p schedcmd.GoalEditPayload
	_ = json.Unmarshal(inv, &p)
	if p.Goals[0].After != nil || p.Goals[1].After.SNRGoal != 10 {
		t.Fatalf("%+v", p)
	}
}

func TestGoalEditRejectsBadValues(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindGoalEdit)
	bad := []string{
		`{"goals":[]}`,
		`{"goals":[{"target_guid":"g","filter":"H-a","before":null,"after":{"kind":0,"snr_goal":0}}]}`,
		`{"goals":[{"target_guid":"g","filter":"H-a","before":null,"after":{"kind":1,"snr_goal":10}}]}`,
		`{"goals":[{"target_guid":"g","filter":"H-a","before":null,"after":{"kind":0,"snr_goal":10,"region":"[{\"x\":2,\"y\":0},{\"x\":0,\"y\":0},{\"x\":0,\"y\":1}]"}}]}`,
		`{"goals":[{"target_guid":"g","filter":"H-a","before":{"kind":0,"snr_goal":10},"after":{"kind":0,"snr_goal":10}}]}`,
	}
	for _, b := range bad {
		if err := s.Validate(json.RawMessage(b)); err == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestRuleWeightTitleAndInverse(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindRuleWeightEdit)
	d, err := s.Describe(json.RawMessage(planningSamples[schedcmd.KindRuleWeightEdit]))
	if err != nil || d.Title != "Cygnis Loop · rule weights" || len(d.Diffs) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	_, inv, _ := s.Inverse(json.RawMessage(planningSamples[schedcmd.KindRuleWeightEdit]))
	var p schedcmd.RuleWeightEditPayload
	_ = json.Unmarshal(inv, &p)
	if p.Changes[0].After != nil || *p.Changes[0].Before != 10 {
		t.Fatalf("%+v", p.Changes[0])
	}
}

func TestTemplateBatchTitle(t *testing.T) {
	t.Parallel()
	d, err := spec(t, schedcmd.KindTemplateBatchEdit).Describe(json.RawMessage(planningSamples[schedcmd.KindTemplateBatchEdit]))
	if err != nil || d.Title != "Moon avoidance separation · 2 templates" || len(d.Diffs) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	if err := spec(t, schedcmd.KindTemplateBatchEdit).Validate(json.RawMessage(`{"items":[{"id":3,"name":"H-a","changes":[{"field":"filtername","before":"a","after":"b"}]}]}`)); err == nil {
		t.Fatal("filtername must not be editable")
	}
}

func TestCloneUndoIsDeleteAndBack(t *testing.T) {
	t.Parallel()
	k, inv, err := spec(t, schedcmd.KindTemplateClone).Inverse(json.RawMessage(planningSamples[schedcmd.KindTemplateClone]))
	if err != nil || k != schedcmd.KindTemplateDelete {
		t.Fatal(err)
	}
	k2, back, err := spec(t, schedcmd.KindTemplateDelete).Inverse(inv)
	if err != nil || k2 != schedcmd.KindTemplateClone {
		t.Fatal(err)
	}
	var c schedcmd.TemplateClonePayload
	_ = json.Unmarshal(back, &c)
	if c.GUID != "t-new" || c.SourceID != 3 {
		t.Fatalf("%+v", c)
	}
}

func TestApplySetDescribe(t *testing.T) {
	t.Parallel()
	d, err := spec(t, schedcmd.KindApplySet).Describe(json.RawMessage(planningSamples[schedcmd.KindApplySet]))
	if err != nil || d.Title != "HOO · 1 target" || len(d.Diffs) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	var after string
	_ = json.Unmarshal(d.Diffs[0].After, &after)
	if after != "add H-a; turn off Luminance" {
		t.Fatalf("%q", after)
	}
	u, err := spec(t, schedcmd.KindUnapplySet).Describe(json.RawMessage(planningSamples[schedcmd.KindApplySet]))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(u.Diffs[0].After, &after)
	if after != "remove H-a; turn on Luminance" {
		t.Fatalf("%q", after)
	}
}

func TestProjectCreateValidation(t *testing.T) {
	t.Parallel()
	s := spec(t, schedcmd.KindProjectCreate)
	d, err := s.Describe(json.RawMessage(planningSamples[schedcmd.KindProjectCreate]))
	if err != nil || d.Title != "Gecko Nebula · project created" {
		t.Fatalf("%+v %v", d, err)
	}
	bad := []string{
		`{"project":{"guid":"p","name":"x"},"targets":[]}`,
		`{"project":{"guid":"p","name":"x"},"targets":[{"guid":"p","name":"t","ra_hours":1,"dec":1}]}`,
		`{"project":{"guid":"p","name":"x"},"targets":[{"guid":"t","name":"t","ra_hours":25,"dec":1}]}`,
		`{"project":{"guid":"p","name":"x"},"targets":[{"guid":"t","name":"t","ra_hours":1,"dec":1}],"goals":[{"target_guid":"zz","filter":"H-a","setting":{"kind":0,"snr_goal":10}}]}`,
	}
	for _, b := range bad {
		if err := s.Validate(json.RawMessage(b)); err == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}

func TestProjectBatchTitle(t *testing.T) {
	t.Parallel()
	d, err := spec(t, schedcmd.KindProjectBatchEdit).Describe(json.RawMessage(planningSamples[schedcmd.KindProjectBatchEdit]))
	if err != nil || d.Title != "Priority · 2 projects" || len(d.Objects) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	var after string
	_ = json.Unmarshal(d.Diffs[0].After, &after)
	if after != "High" {
		t.Fatalf("%q", after)
	}
}
