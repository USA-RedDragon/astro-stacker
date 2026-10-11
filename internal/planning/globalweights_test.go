package planning

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ruleweights"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

func weightsDBs(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	sched, appDB := testDBs(t)
	for _, q := range []string{
		`delete from ruleweight`,
		`insert into project values (8,'p','Heart',null,1,1,60,20,0,0,1,'pg8'),(6,'p','Veil',null,2,1,60,20,0,1,1,'pg6')`,
		`insert into ruleweight (name, weight, projectid) values
			('Novelty',10,5),('Novelty',10,6),('Novelty',35,7),('Novelty',35,8),
			('Panel Deficit',25,5),('Panel Deficit',25,6),('Panel Deficit',0,7),('Panel Deficit',0,8),
			('Rarity',75,5),('Rarity',75,6),('Rarity',75,7),('Rarity',0,8),
			('Filter Steering',100,5)`,
	} {
		if err := sched.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := appDB.AutoMigrate(&app.GlobalRuleWeight{}); err != nil {
		t.Fatal(err)
	}
	return sched, appDB
}

const rarity = "Rarity"

func load(t *testing.T, sched, appDB *gorm.DB) *Snapshot {
	t.Helper()
	s, err := Load(context.Background(), sched, appDB, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGlobalRulesCoverEveryRule(t *testing.T) {
	t.Parallel()
	g := GlobalRules()
	if len(g) != len(ruleweights.Keys()) {
		t.Fatalf("%d rules for %d keys", len(g), len(ruleweights.Keys()))
	}
	for _, r := range Rules() {
		if !ruleweights.Known(r.Name) {
			t.Errorf("%s is missing from the global keys", r.Name)
		}
	}
	for _, r := range g {
		if r.Description == "" || r.Label == "" {
			t.Errorf("%+v", r)
		}
	}
	nm := globalRule(ruleweights.NoveltyMosaic)
	pd := globalRule(ruleweights.PanelDeficit)
	fs := globalRule(ruleweights.FilterSteering)
	if nm.Rule != ruleweights.Novelty || nm.For != WeightsForMosaic || !pd.MosaicOnly || pd.DefaultWeight != 75 || !fs.Switch {
		t.Fatalf("%+v %+v %+v", nm, pd, fs)
	}
}

func TestGlobalWeightsFollowProjectsUntilSaved(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	s := load(t, sched, appDB)
	g := s.GlobalWeights
	want := map[string]float64{ruleweights.Novelty: 35, ruleweights.NoveltyMosaic: 10, ruleweights.PanelDeficit: 25, rarity: 75, ruleweights.FilterSteering: 0}
	for k, w := range want {
		if g.Weights[k] != w {
			t.Errorf("%s = %v, want %v", k, g.Weights[k], w)
		}
	}
	if g.Basis != WeightsBasisProjects || g.Projects != 4 || g.Mosaics != 2 || len(g.Saved) != 0 {
		t.Fatalf("%+v", g)
	}
	cyg, _ := s.Project(5)
	keys := make([]string, 0, len(cyg.WeightDiffs))
	for _, d := range cyg.WeightDiffs {
		keys = append(keys, d.Key)
	}
	if !reflect.DeepEqual(keys, []string{ruleweights.FilterSteering}) {
		t.Fatalf("project 5 differs on %v", keys)
	}
	if s.Defaults.PanelDeficitWeight != 25 {
		t.Fatalf("the wizard's Panel Deficit is the global one: %v", s.Defaults.PanelDeficitWeight)
	}

	ctx := context.Background()
	if err := ruleweights.Apply(ctx, appDB, []ruleweights.Change{{Rule: ruleweights.NoveltyMosaic, After: fp(5)}}, "jacob", time.Now()); err != nil {
		t.Fatal(err)
	}
	g = load(t, sched, appDB).GlobalWeights
	if g.Basis != WeightsBasisSaved || g.Weights[ruleweights.NoveltyMosaic] != 5 || g.Weights[ruleweights.Novelty] != 35 || g.SavedBy != "jacob" || g.Saved[ruleweights.NoveltyMosaic] != 5 {
		t.Fatalf("%+v", g)
	}
}

func TestProjectDiffsCountMissingRowsAsZero(t *testing.T) {
	t.Parallel()
	global := map[string]float64{ruleweights.Novelty: 35, ruleweights.NoveltyMosaic: 5, rarity: 0, ruleweights.PanelDeficit: 25}
	rows := map[string]float64{ruleweights.Novelty: 5, ruleweights.PanelDeficit: 25}
	d := ProjectDiffs(global, rows, true)
	if len(d) != 0 {
		t.Fatalf("a mosaic takes Novelty (mosaic) and a missing Rarity row is 0: %+v", d)
	}
	d = ProjectDiffs(global, rows, false)
	if len(d) != 1 || d[0].Key != ruleweights.Novelty || *d[0].Project != 5 || d[0].Global != 35 {
		t.Fatalf("%+v", d)
	}
	d = ProjectDiffs(map[string]float64{rarity: 20}, nil, false)
	if len(d) != 1 || d[0].Project != nil || d[0].Rule != rarity {
		t.Fatalf("%+v", d)
	}
}

func TestWeightsForSplitsNovelty(t *testing.T) {
	t.Parallel()
	global := map[string]float64{ruleweights.Novelty: 35, ruleweights.NoveltyMosaic: 5, ruleweights.PanelDeficit: 25}
	m, s := WeightsFor(global, true), WeightsFor(global, false)
	if m[ruleweights.Novelty] != 5 || s[ruleweights.Novelty] != 35 || m[ruleweights.PanelDeficit] != 25 || s[ruleweights.PanelDeficit] != 25 {
		t.Fatalf("%v %v", m, s)
	}
	if _, ok := m[ruleweights.NoveltyMosaic]; ok || len(m) != len(Rules())+2 {
		t.Fatalf("only Target Scheduler rule names reach a project: %v", m)
	}
}

func TestSyncEdits(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	s := load(t, sched, appDB)
	all, err := s.SyncEdits(nil)
	if err != nil || len(all) != len(s.GlobalWeights.Differing) || len(all) == 0 {
		t.Fatalf("%d %v", len(all), err)
	}
	one, err := s.SyncEdits([]int{8})
	if err != nil || len(one) != 1 || one[0].ProjectID != 8 || one[0].ProjectGUID != "pg8" || one[0].ProjectName != "Heart" {
		t.Fatalf("%+v %v", one, err)
	}
	byRule := map[string][2]*float64{}
	for _, c := range one[0].Changes {
		byRule[c.Rule] = [2]*float64{c.Before, c.After}
	}
	if r := byRule[rarity]; *r[0] != 0 || *r[1] != 75 {
		t.Fatalf("Rarity %v", r)
	}
	if r := byRule[ruleweights.PanelDeficit]; len(byRule) != 2 || *r[0] != 0 || *r[1] != 25 {
		t.Fatalf("missing rows and a global 0 agree; Panel Deficit reaches single targets too: %v", byRule)
	}
	if _, err := s.SyncEdits([]int{404}); err == nil {
		t.Fatal("an unknown project should fail")
	}
}

func TestGlobalChanges(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	s := load(t, sched, appDB)
	if _, err := s.GlobalChanges(map[string]float64{"Made Up": 1}); err == nil {
		t.Fatal("unknown key")
	}
	if _, err := s.GlobalChanges(map[string]float64{rarity: 120}); err == nil {
		t.Fatal("out of range")
	}
	p, err := s.GlobalChanges(map[string]float64{ruleweights.Novelty: 40})
	if err != nil || len(p.Changes) != len(ruleweights.Keys()) {
		t.Fatalf("the first save keeps the whole set: %+v %v", p, err)
	}
	for _, c := range p.Changes {
		if c.Before != nil {
			t.Fatalf("nothing is saved yet: %+v", c)
		}
		if c.Rule == ruleweights.Novelty && *c.After != 40 || c.Rule == ruleweights.NoveltyMosaic && *c.After != 10 {
			t.Fatalf("%+v", c)
		}
	}
	if err := ruleweights.Apply(context.Background(), appDB, p.Changes, "web", time.Now()); err != nil {
		t.Fatal(err)
	}
	s = load(t, sched, appDB)
	p, err = s.GlobalChanges(map[string]float64{ruleweights.Novelty: 40, rarity: 20})
	if err != nil || len(p.Changes) != 1 || p.Changes[0].Rule != rarity || *p.Changes[0].Before != 75 || *p.Changes[0].After != 20 {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err = s.GlobalChanges(map[string]float64{ruleweights.Novelty: 40}); len(p.Changes) != 0 || err != nil {
		t.Fatalf("no change: %+v %v", p, err)
	}
}

func TestWhatIfWeights(t *testing.T) {
	t.Parallel()
	mosaic := map[int]bool{5: true, 6: true, 7: false, 8: false}
	got, err := WhatIfWeights(map[string]float64{rarity: 20, ruleweights.NoveltyMosaic: 5, ruleweights.Novelty: 40}, mosaic)
	if err != nil {
		t.Fatal(err)
	}
	want := []WeightOverride{
		{ProjectID: 7, Rule: ruleweights.Novelty, Weight: 40}, {ProjectID: 8, Rule: ruleweights.Novelty, Weight: 40},
		{ProjectID: 5, Rule: ruleweights.Novelty, Weight: 5}, {ProjectID: 6, Rule: ruleweights.Novelty, Weight: 5},
		{Rule: rarity, Weight: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if _, err := WhatIfWeights(map[string]float64{rarity: -1}, mosaic); err == nil {
		t.Fatal("out of range")
	}
}

func TestMosaicFlags(t *testing.T) {
	t.Parallel()
	sched, _ := weightsDBs(t)
	m, err := MosaicFlags(context.Background(), sched)
	if err != nil || !reflect.DeepEqual(m, map[int]bool{5: true, 6: true, 7: false, 8: false}) {
		t.Fatalf("%v %v", m, err)
	}
}

func TestDraftProjectTakesTheGlobalSet(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	if err := ruleweights.Apply(context.Background(), appDB, []ruleweights.Change{{Rule: ruleweights.NoveltyMosaic, After: fp(5)}, {Rule: ruleweights.Novelty, After: fp(40)}}, "web", time.Now()); err != nil {
		t.Fatal(err)
	}
	s := load(t, sched, appDB)
	hoo := setNamed(t, s, setHOO).ID
	m, err := s.DraftProject(ProjectDraft{Name: "Gecko", SetID: hoo, MinimumTime: 60, Panels: []PanelDraft{{RAHours: 22.5, Dec: 40.8}, {RAHours: 22.7, Dec: 40.8}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	one, err := s.DraftProject(ProjectDraft{Name: "Pelican", SetID: hoo, MinimumTime: 60, Panels: []PanelDraft{{RAHours: 20.8, Dec: 44.3}}}, seq())
	if err != nil {
		t.Fatal(err)
	}
	if m.RuleWeights[ruleweights.Novelty] != 5 || one.RuleWeights[ruleweights.Novelty] != 40 || m.RuleWeights[ruleweights.PanelDeficit] != 25 || one.RuleWeights[rarity] != 75 {
		t.Fatalf("%v %v", m.RuleWeights, one.RuleWeights)
	}
}

func TestGlobalWithRecomparesProjects(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	s := load(t, sched, appDB)
	before := len(s.GlobalWeights.Differing)
	g, err := s.GlobalWith(map[string]float64{ruleweights.NoveltyMosaic: 10, ruleweights.Novelty: 0})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int]bool{}
	for _, d := range g.Differing {
		ids[d.ID] = true
	}
	if !ids[7] || !ids[8] || g.Weights[ruleweights.Novelty] != 0 || s.GlobalWeights.Weights[ruleweights.Novelty] != 35 || len(s.GlobalWeights.Differing) != before {
		t.Fatalf("%v %+v", ids, g.Weights)
	}
	if _, err := s.GlobalWith(map[string]float64{rarity: 101}); err == nil {
		t.Fatal("out of range")
	}
}

func TestEvenPanelsIsAMosaicSwitch(t *testing.T) {
	t.Parallel()
	keys := ruleweights.Keys()
	if i := slices.Index(keys, ruleweights.EvenPanels); i < 1 || keys[i-1] != ruleweights.PanelDeficit {
		t.Fatalf("Even Panels should follow Panel Deficit: %v", keys)
	}
	g := globalRule(ruleweights.EvenPanels)
	if g.Rule != ruleweights.EvenPanels || !g.Switch || !g.MosaicOnly || g.DefaultWeight != 0 || g.For != WeightsForAll || !g.New || g.Description == "" {
		t.Fatalf("%+v", g)
	}
	if slices.ContainsFunc(Rules(), func(r Rule) bool { return r.Name == ruleweights.EvenPanels }) {
		t.Fatal("Even Panels is a switch, not a scored rule")
	}
}

func TestEvenPanelsSyncsLikeAnyGlobalRule(t *testing.T) {
	t.Parallel()
	sched, appDB := weightsDBs(t)
	if err := sched.Exec(`insert into ruleweight (name, weight, projectid) values ('Even Panels',100,5)`).Error; err != nil {
		t.Fatal(err)
	}
	s := load(t, sched, appDB)
	if w, ok := s.GlobalWeights.Weights[ruleweights.EvenPanels]; !ok || w != 0 {
		t.Fatalf("global Even Panels %v %v", w, ok)
	}
	cyg, _ := s.Project(5)
	if slices.ContainsFunc(cyg.RuleWeights, func(w RuleWeight) bool { return w.Name == ruleweights.EvenPanels }) {
		t.Fatalf("Even Panels in the scored rule weights %+v", cyg.RuleWeights)
	}
	k := slices.IndexFunc(cyg.WeightDiffs, func(d WeightDiff) bool { return d.Key == ruleweights.EvenPanels })
	if k < 0 || cyg.WeightDiffs[k].Project == nil || *cyg.WeightDiffs[k].Project != 100 || cyg.WeightDiffs[k].Global != 0 {
		t.Fatalf("project 5 diffs %+v", cyg.WeightDiffs)
	}
	on, err := s.GlobalWith(map[string]float64{ruleweights.EvenPanels: 100})
	if err != nil {
		t.Fatal(err)
	}
	veil := slices.IndexFunc(on.Differing, func(p ProjectWeights) bool { return p.ID == 6 })
	if veil < 0 {
		t.Fatalf("differing %+v", on.Differing)
	}
	d := on.Differing[veil].Diffs
	if k := slices.IndexFunc(d, func(x WeightDiff) bool { return x.Key == ruleweights.EvenPanels }); k < 0 || d[k].Project != nil || d[k].Global != 100 {
		t.Fatalf("a missing row should be created on sync: %+v", d)
	}
	edits, err := s.SyncEdits([]int{5})
	if err != nil || len(edits) != 1 {
		t.Fatalf("%+v %v", edits, err)
	}
	if k := slices.IndexFunc(edits[0].Changes, func(c schedcmd.RuleWeightChange) bool { return c.Rule == ruleweights.EvenPanels }); k < 0 || *edits[0].Changes[k].Before != 100 || *edits[0].Changes[k].After != 0 {
		t.Fatalf("sync %+v", edits[0].Changes)
	}
	over, err := WhatIfWeights(map[string]float64{ruleweights.EvenPanels: 100}, map[int]bool{5: true, 7: false})
	if err != nil || !reflect.DeepEqual(over, []WeightOverride{{Rule: ruleweights.EvenPanels, Weight: 100}}) {
		t.Fatalf("what-if %+v %v", over, err)
	}
}
