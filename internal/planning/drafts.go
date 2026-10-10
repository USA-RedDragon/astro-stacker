package planning

import (
	"errors"
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

var (
	ErrUnknownSet      = errors.New("unknown exposure set")
	ErrMissingTemplate = errors.New("the observatory has no template for this set")
	ErrNoTargets       = errors.New("no targets chosen")
)

const DefaultDesired = 300

type TargetEffect struct {
	TargetID int    `json:"targetId"`
	Name     string `json:"name"`
	Project  string `json:"project"`
	Current  string `json:"current"`
	Effect   string `json:"effect"`
	Changes  bool   `json:"changes"`
}

type ApplySetDraft struct {
	Payload *schedcmd.ApplySetPayload `json:"payload,omitempty"`
	Effects []TargetEffect            `json:"effects"`
	Missing []string                  `json:"missing,omitempty"`
}

func (s *Snapshot) templateByName(name string) (Template, bool) {
	n := normTemplate(name)
	for _, t := range s.Templates {
		if normTemplate(t.Name) == n {
			return t, true
		}
	}
	return Template{}, false
}

func (s *Snapshot) target(id int) (Target, Project, bool) {
	for _, p := range s.Projects {
		for _, t := range p.Targets {
			if t.ID == id {
				return t, p, true
			}
		}
	}
	return Target{}, Project{}, false
}

func joinOr(list []string, none string) string {
	if len(list) == 0 {
		return none
	}
	return strings.Join(list, ", ")
}

func (s *Snapshot) DraftApplySet(setID, mode string, targetIDs []int, desired int, newID func() string) (ApplySetDraft, error) {
	set, ok := SetByID(setID)
	if !ok {
		return ApplySetDraft{}, ErrUnknownSet
	}
	if mode != "add" && mode != "replace" {
		return ApplySetDraft{}, fmt.Errorf("mode must be add or replace")
	}
	if len(targetIDs) == 0 {
		return ApplySetDraft{}, ErrNoTargets
	}
	if desired <= 0 {
		desired = DefaultDesired
	}
	out := ApplySetDraft{}
	type want struct {
		tmpl Template
		exp  float64
	}
	var wants []want
	for _, it := range set.Items {
		t, ok := s.templateByName(it.Template)
		if !ok {
			out.Missing = append(out.Missing, it.Template)
			continue
		}
		wants = append(wants, want{t, it.Exposure})
	}
	if len(out.Missing) > 0 {
		return out, fmt.Errorf("%w: %s", ErrMissingTemplate, strings.Join(out.Missing, ", "))
	}
	payload := &schedcmd.ApplySetPayload{SetID: set.ID, SetName: set.Name, Mode: mode}
	for _, id := range targetIDs {
		t, p, ok := s.target(id)
		if !ok {
			return out, fmt.Errorf("unknown target %d", id)
		}
		at := schedcmd.ApplySetTarget{TargetID: int64(t.ID), TargetGUID: t.GUID, TargetName: t.Name, Project: p.Name}
		var adds, ons, offs []string
		inSet := map[int]bool{}
		for _, w := range wants {
			var enabled, disabled *Plan
			for i := range t.Plans {
				pl := &t.Plans[i]
				if pl.TemplateID != w.tmpl.ID {
					continue
				}
				if pl.Enabled && enabled == nil {
					enabled = pl
				} else if !pl.Enabled && disabled == nil {
					disabled = pl
				}
			}
			switch {
			case enabled != nil:
				inSet[enabled.ID] = true
			case disabled != nil:
				inSet[disabled.ID] = true
				at.Enable = append(at.Enable, schedcmd.PlanRef{ID: int64(disabled.ID), GUID: disabled.GUID, TemplateName: w.tmpl.Name})
				ons = append(ons, w.tmpl.Name)
			default:
				at.Create = append(at.Create, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(w.tmpl.ID), TemplateName: w.tmpl.Name, Exposure: w.exp, Desired: desired})
				adds = append(adds, w.tmpl.Name)
			}
		}
		if mode == "replace" {
			for _, pl := range t.Plans {
				if pl.Enabled && !inSet[pl.ID] {
					at.Disable = append(at.Disable, schedcmd.PlanRef{ID: int64(pl.ID), GUID: pl.GUID, TemplateName: pl.Template})
					offs = append(offs, pl.Template)
				}
			}
		}
		var parts []string
		if len(adds) > 0 {
			parts = append(parts, "adds "+strings.Join(adds, ", "))
		}
		if len(ons) > 0 {
			parts = append(parts, "turns on "+strings.Join(ons, ", "))
		}
		if len(offs) > 0 {
			parts = append(parts, "turns off "+strings.Join(offs, ", ")+" (counts kept)")
		}
		eff := TargetEffect{TargetID: t.ID, Name: t.Name, Project: p.Name, Current: t.SetName, Changes: len(parts) > 0}
		if eff.Changes {
			eff.Effect = joinOr(parts, "")
			payload.Targets = append(payload.Targets, at)
		} else {
			eff.Effect = "Already has this set; nothing changes"
		}
		out.Effects = append(out.Effects, eff)
	}
	if len(payload.Targets) > 0 {
		out.Payload = payload
	}
	return out, nil
}

type PanelDraft struct {
	Name     string  `json:"name"`
	RAHours  float64 `json:"raHours"`
	Dec      float64 `json:"dec"`
	Rotation float64 `json:"rotation"`
}

type GoalDraft struct {
	Kind        string  `json:"kind"`
	SNR         float64 `json:"snr"`
	Depth       float64 `json:"depth"`
	PlateauStop *bool   `json:"plateauStop"`
}

type ProjectDraft struct {
	Name            string       `json:"name"`
	Catalog         string       `json:"catalog"`
	Match           string       `json:"match"`
	MatchWith       string       `json:"matchWith"`
	Priority        string       `json:"priority"`
	MinimumAltitude float64      `json:"minimumAltitude"`
	MinimumTime     int          `json:"minimumTime"`
	SetID           string       `json:"setId"`
	Desired         int          `json:"desired"`
	Goal            GoalDraft    `json:"goal"`
	Panels          []PanelDraft `json:"panels"`
}

func priorityIndex(s string) int {
	for i, p := range Priorities {
		if strings.EqualFold(p, s) {
			return i
		}
	}
	return 1
}

func (s *Snapshot) DraftProject(d ProjectDraft, newID func() string) (*schedcmd.ProjectCreatePayload, error) {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return nil, errors.New("the project needs a name")
	}
	for _, p := range s.Projects {
		if strings.EqualFold(p.Name, name) && d.Match != "separate" {
			return nil, fmt.Errorf("a project called %s already exists", p.Name)
		}
	}
	if len(d.Panels) == 0 {
		return nil, errors.New("no panels")
	}
	set, ok := SetByID(d.SetID)
	if !ok {
		return nil, ErrUnknownSet
	}
	desired := d.Desired
	if desired <= 0 {
		desired = DefaultDesired
	}
	minTime := d.MinimumTime
	if minTime <= 0 {
		minTime = 60
	}
	minAlt := d.MinimumAltitude
	if minAlt <= 0 {
		minAlt = 15
	}
	mosaic := len(d.Panels) > 1
	desc := ""
	if d.Catalog != "" {
		desc = "Catalogue: " + d.Catalog
	}
	out := &schedcmd.ProjectCreatePayload{
		Project: schedcmd.NewProject{GUID: newID(), Name: name, Description: desc, Priority: priorityIndex(d.Priority), State: 1,
			MinimumTime: minTime, MinimumAltitude: minAlt, IsMosaic: mosaic},
		Catalog: d.Catalog, Match: d.Match, MatchWith: d.MatchWith,
		RuleWeights: map[string]float64{},
	}
	for _, r := range Rules() {
		out.RuleWeights[r.Name] = r.DefaultWeight
	}
	if mosaic {
		out.RuleWeights["Panel Deficit"] = 75
	}
	plateau := true
	if d.Goal.PlateauStop != nil {
		plateau = *d.Goal.PlateauStop
	}
	setting := schedcmd.GoalSetting{Kind: schedcmd.GoalKindSNR, SNRGoal: d.Goal.SNR, PlateauStop: plateau}
	if setting.SNRGoal <= 0 {
		setting.SNRGoal = 10
	}
	if d.Goal.Kind == "depth" {
		depth := d.Goal.Depth
		setting.Kind = schedcmd.GoalKindDepth
		setting.DepthGoal = &depth
	}
	for i, pd := range d.Panels {
		tname := strings.TrimSpace(pd.Name)
		if tname == "" {
			tname = name
			if mosaic {
				tname = fmt.Sprintf("%s Panel %d", name, i+1)
			}
		}
		nt := schedcmd.NewTarget{GUID: newID(), Name: tname, RAHours: pd.RAHours, Dec: pd.Dec, Rotation: pd.Rotation}
		filters := []string{}
		for _, it := range set.Items {
			t, ok := s.templateByName(it.Template)
			if !ok {
				return nil, fmt.Errorf("%w: %s", ErrMissingTemplate, it.Template)
			}
			nt.Plans = append(nt.Plans, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(t.ID), TemplateName: t.Name, Exposure: it.Exposure, Desired: desired})
			if !containsFold(filters, t.Filter) {
				filters = append(filters, t.Filter)
				g := setting
				if g.Kind == schedcmd.GoalKindDepth && d.Goal.Depth <= 0 {
					dd := defaultDepth(t.Filter)
					g.DepthGoal = &dd
				}
				out.Goals = append(out.Goals, schedcmd.NewGoal{TargetGUID: nt.GUID, Filter: t.Filter, Setting: g})
			}
		}
		out.Targets = append(out.Targets, nt)
	}
	return out, nil
}

func defaultDepth(filter string) float64 {
	switch frameheader.NormalizeFilter(filter) {
	case "H-a", "O-III", "S-II":
		return 25.5
	}
	return 25.8
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
