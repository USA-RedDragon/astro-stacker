package planning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/ruleweights"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"gorm.io/gorm"
)

const (
	WeightsBasisSaved    = "saved"
	WeightsBasisProjects = "projects"
	WeightsBasisDefaults = "defaults"

	WeightsForAll    = "all"
	WeightsForMosaic = "mosaic"
	WeightsForSingle = "single"
)

// GlobalRule is one weight of the global set. Rule is the Target Scheduler
// rule it is stored under; For says which projects take it; MosaicOnly marks
// rules that score 0 on anything but a mosaic panel.
type GlobalRule struct {
	Key           string  `json:"key"`
	Rule          string  `json:"rule"`
	Label         string  `json:"label"`
	DefaultWeight float64 `json:"defaultWeight"`
	New           bool    `json:"new"`
	Switch        bool    `json:"switch"`
	For           string  `json:"for"`
	MosaicOnly    bool    `json:"mosaicOnly"`
	Description   string  `json:"description"`
}

func GlobalRules() []GlobalRule {
	byName := map[string]Rule{}
	for _, r := range Rules() {
		byName[r.Name] = r
	}
	out := make([]GlobalRule, 0, len(ruleweights.Keys()))
	for _, key := range ruleweights.Keys() {
		g := GlobalRule{Key: key, Rule: ruleweights.Rule(key), Label: key, For: WeightsForAll}
		if r, ok := byName[g.Rule]; ok {
			g.DefaultWeight, g.New, g.Description = r.DefaultWeight, r.New, r.Description
		}
		switch key {
		case ruleweights.Novelty:
			g.Label, g.For = "Novelty, single targets", WeightsForSingle
		case ruleweights.NoveltyMosaic:
			g.Label, g.For = "Novelty, mosaics", WeightsForMosaic
		case ruleweights.PanelDeficit:
			g.DefaultWeight, g.MosaicOnly = mosaics.PanelDeficitOnWeight, true
		case ruleweights.EvenPanels:
			g.DefaultWeight, g.Switch, g.MosaicOnly, g.New = 0, true, true, true
			g.Description = "Not a scored rule: on, Panel Deficit shares this season's imaging hours out evenly across a mosaic's unfinished panels instead of favouring the weakest panel. Each panel's fair share grows with its number of unfinished filters. Off, Panel Deficit favours the weakest panel. Does nothing while Panel Deficit is 0."
		case "Mosaic Completion", RuleSeasonPriority:
			g.MosaicOnly = true
		case ruleweights.FilterSteering:
			g.Switch = true
			g.Description = "Not a scored rule: on, each target and panel shoots the filter furthest behind its goal that still gains most per hour, keeping broadband out of a bright moon. Targets without goals always follow the exposure order."
		}
		out = append(out, g)
	}
	return out
}

func globalRule(key string) GlobalRule {
	for _, g := range GlobalRules() {
		if g.Key == key {
			return g
		}
	}
	return GlobalRule{}
}

// WeightDiff is a rule where a project's row differs from the global set.
// Project is nil when the project has no row for the rule.
type WeightDiff struct {
	Key     string   `json:"key"`
	Rule    string   `json:"rule"`
	Project *float64 `json:"project"`
	Global  float64  `json:"global"`
}

type ProjectWeights struct {
	ID       int          `json:"id"`
	GUID     string       `json:"guid"`
	Name     string       `json:"name"`
	State    string       `json:"state"`
	IsMosaic bool         `json:"isMosaic"`
	Diffs    []WeightDiff `json:"diffs"`
}

type GlobalWeights struct {
	Rules     []GlobalRule       `json:"rules"`
	Weights   map[string]float64 `json:"weights"`
	Basis     string             `json:"basis"`
	SavedAt   *time.Time         `json:"savedAt,omitempty"`
	SavedBy   string             `json:"savedBy,omitempty"`
	Saved     map[string]float64 `json:"saved"`
	Projects  int                `json:"projects"`
	Mosaics   int                `json:"mosaics"`
	Differing []ProjectWeights   `json:"differing"`
}

func takes(g GlobalRule, mosaic bool) bool {
	switch g.For {
	case WeightsForMosaic:
		return mosaic
	case WeightsForSingle:
		return !mosaic
	}
	return true
}

func deriveWeights(projects []projectRow, weightsBy map[int]map[string]float64) (map[string]float64, string) {
	out := map[string]float64{}
	basis := WeightsBasisDefaults
	for _, g := range GlobalRules() {
		var pool []projectRow
		for _, p := range projects {
			if takes(g, p.IsMosaic != 0) && (!g.MosaicOnly || p.IsMosaic != 0) {
				pool = append(pool, p)
			}
		}
		if len(pool) == 0 && g.MosaicOnly {
			pool = projects
		}
		if len(pool) > 0 {
			basis = WeightsBasisProjects
		}
		out[g.Key] = majority(pool, func(r projectRow) *float64 {
			w := weightsBy[r.ID][g.Rule]
			return &w
		}, g.DefaultWeight)
	}
	return out, basis
}

func buildGlobalWeights(saved ruleweights.Saved, projects []projectRow, weightsBy map[int]map[string]float64) GlobalWeights {
	derived, basis := deriveWeights(projects, weightsBy)
	g := GlobalWeights{Rules: GlobalRules(), Weights: derived, Basis: basis, Saved: map[string]float64{}, Differing: []ProjectWeights{}}
	for k, w := range saved.Weights {
		g.Weights[k], g.Saved[k] = w, w
	}
	if len(saved.Weights) > 0 {
		g.Basis, g.SavedAt, g.SavedBy = WeightsBasisSaved, saved.At, saved.By
	}
	return g
}

func (g *GlobalWeights) compare(projects []Project, weightsBy map[int]map[string]float64) {
	g.Projects, g.Mosaics, g.Differing = len(projects), 0, []ProjectWeights{}
	for _, p := range projects {
		if p.IsMosaic {
			g.Mosaics++
		}
		if d := ProjectDiffs(g.Weights, weightsBy[p.ID], p.IsMosaic); len(d) > 0 {
			g.Differing = append(g.Differing, ProjectWeights{ID: p.ID, GUID: p.GUID, Name: p.Name, State: p.State, IsMosaic: p.IsMosaic, Diffs: d})
		}
	}
}

// GlobalWith is the global set with new values for some keys, and the
// projects that would then differ from it. Nothing is saved.
func (s *Snapshot) GlobalWith(weights map[string]float64) (GlobalWeights, error) {
	for k, w := range weights {
		if err := ruleweights.Check(k, w); err != nil {
			return GlobalWeights{}, err
		}
	}
	g := s.GlobalWeights
	g.Weights = make(map[string]float64, len(s.GlobalWeights.Weights))
	for k, w := range s.GlobalWeights.Weights {
		g.Weights[k] = w
	}
	for k, w := range weights {
		g.Weights[k] = w
	}
	g.compare(s.Projects, s.weightsBy)
	return g, nil
}

// ProjectDiffs lists the rules where a project's rows differ from the global
// set. A missing row counts as weight 0, as Target Scheduler scores it.
func ProjectDiffs(weights map[string]float64, rows map[string]float64, mosaic bool) []WeightDiff {
	out := []WeightDiff{}
	for _, g := range GlobalRules() {
		if !takes(g, mosaic) {
			continue
		}
		want := weights[g.Key]
		have, ok := rows[g.Rule]
		if ruleweights.Same(rows[g.Rule], want) {
			continue
		}
		d := WeightDiff{Key: g.Key, Rule: g.Rule, Global: want}
		if ok {
			d.Project = &have
		}
		out = append(out, d)
	}
	return out
}

// WeightsFor is the rule weights a project of the given kind takes from the
// global set, by Target Scheduler rule name.
func WeightsFor(weights map[string]float64, mosaic bool) map[string]float64 {
	out := map[string]float64{}
	for _, g := range GlobalRules() {
		if takes(g, mosaic) {
			out[g.Rule] = weights[g.Key]
		}
	}
	return out
}

var ErrNoProject = errors.New("no such project")

// SyncEdits builds the ruleweight.edit payloads that make projects equal to
// the global set: every differing project, or only those in ids.
func (s *Snapshot) SyncEdits(ids []int) ([]schedcmd.RuleWeightEditPayload, error) {
	for _, id := range ids {
		if _, ok := s.Project(id); !ok {
			return nil, fmt.Errorf("%w: %d", ErrNoProject, id)
		}
	}
	var out []schedcmd.RuleWeightEditPayload
	for _, d := range s.GlobalWeights.Differing {
		if len(ids) > 0 && !slices.Contains(ids, d.ID) {
			continue
		}
		p := schedcmd.RuleWeightEditPayload{ProjectID: int64(d.ID), ProjectGUID: d.GUID, ProjectName: d.Name}
		for _, x := range d.Diffs {
			after := x.Global
			p.Changes = append(p.Changes, schedcmd.RuleWeightChange{Rule: x.Rule, Before: x.Project, After: &after})
		}
		out = append(out, p)
	}
	return out, nil
}

// GlobalChanges turns new values for some keys into a ruleweight.global
// payload, with no changes when nothing differs. When nothing is saved yet
// the whole set is saved, so the global set stops following the projects.
func (s *Snapshot) GlobalChanges(weights map[string]float64) (schedcmd.GlobalWeightsPayload, error) {
	for k, w := range weights {
		if err := ruleweights.Check(k, w); err != nil {
			return schedcmd.GlobalWeightsPayload{}, err
		}
	}
	g := s.GlobalWeights
	p := schedcmd.GlobalWeightsPayload{}
	for _, r := range g.Rules {
		want, changed := weights[r.Key]
		saved, isSaved := g.Saved[r.Key]
		if !changed {
			if isSaved {
				continue
			}
			want = g.Weights[r.Key]
		}
		var before *float64
		if isSaved {
			before = &saved
			if ruleweights.Same(saved, want) {
				continue
			}
		}
		after := want
		p.Changes = append(p.Changes, ruleweights.Change{Rule: r.Key, Before: before, After: &after})
	}
	return p, nil
}

// WeightOverride is one what-if rule weight for the scheduler preview.
// ProjectID 0 means every project.
type WeightOverride struct {
	ProjectID int
	Rule      string
	Weight    float64
}

// WhatIfWeights expands global weight changes into preview overrides. Keys
// that only some projects take become one override per such project.
func WhatIfWeights(weights map[string]float64, mosaic map[int]bool) ([]WeightOverride, error) {
	for k, w := range weights {
		if err := ruleweights.Check(k, w); err != nil {
			return nil, err
		}
	}
	var out []WeightOverride
	for _, key := range ruleweights.Keys() {
		w, ok := weights[key]
		if !ok {
			continue
		}
		g := globalRule(key)
		if g.For == WeightsForAll {
			out = append(out, WeightOverride{Rule: g.Rule, Weight: w})
			continue
		}
		ids := make([]int, 0, len(mosaic))
		for id, m := range mosaic {
			if takes(g, m) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			out = append(out, WeightOverride{ProjectID: id, Rule: g.Rule, Weight: w})
		}
	}
	return out, nil
}

// MosaicFlags reads which projects are mosaics, by project id.
func MosaicFlags(ctx context.Context, sched *gorm.DB) (map[int]bool, error) {
	var rows []projectRow
	if err := sched.WithContext(ctx).Select(`"Id"`, `"isMosaic"`).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(rows))
	for _, r := range rows {
		out[r.ID] = r.IsMosaic != 0
	}
	return out, nil
}
