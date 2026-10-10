package planning

import (
	"errors"
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

var (
	ErrUnknownSet      = errors.New("unknown exposure set")
	ErrMissingTemplate = errors.New("the observatory has no template for this set")
	ErrNoTargets       = errors.New("no targets chosen")
	ErrNoDesired       = errors.New("no desired count")
	ErrNoExposure      = errors.New("no sub length")
)

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
	set, ok := s.SetByID(setID)
	if !ok {
		return ApplySetDraft{}, ErrUnknownSet
	}
	if mode != "add" && mode != "replace" {
		return ApplySetDraft{}, fmt.Errorf("mode must be add or replace")
	}
	if len(targetIDs) == 0 {
		return ApplySetDraft{}, ErrNoTargets
	}
	out := ApplySetDraft{}
	type want struct {
		tmpl    Template
		exp     float64
		desired int
	}
	var wants []want
	for _, it := range set.Items {
		t, ok := s.templateByName(it.Template)
		if !ok {
			out.Missing = append(out.Missing, it.Template)
			continue
		}
		n, err := desiredFor(desired, it)
		if err != nil {
			return out, err
		}
		wants = append(wants, want{t, it.Exposure, n})
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
				at.Create = append(at.Create, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(w.tmpl.ID), TemplateName: w.tmpl.Name, Exposure: w.exp, Desired: w.desired})
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

type MosaicDraft struct {
	Layout   string  `json:"layout"`
	Rotation float64 `json:"rotation"`
	Overlap  float64 `json:"overlap"`
	Cols     int     `json:"cols"`
	Rows     int     `json:"rows"`
}

type PlanDraft struct {
	TemplateID int     `json:"templateId"`
	Exposure   float64 `json:"exposure"`
}

type ProjectDraft struct {
	Mosaic          *MosaicDraft `json:"mosaic"`
	Name            string       `json:"name"`
	Catalog         string       `json:"catalog"`
	Match           string       `json:"match"`
	MatchWith       string       `json:"matchWith"`
	Priority        string       `json:"priority"`
	MinimumAltitude float64      `json:"minimumAltitude"`
	MinimumTime     int          `json:"minimumTime"`
	SetID           string       `json:"setId"`
	Plans           []PlanDraft  `json:"plans"`
	GoalDriven      bool         `json:"goalDriven"`
	Desired         int          `json:"desired"`
	Goal            GoalDraft    `json:"goal"`
	Panels          []PanelDraft `json:"panels"`
}

func priorityIndex(s string) int {
	for i, p := range Priorities() {
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
	items, err := s.draftItems(d)
	if err != nil {
		return nil, err
	}
	minTime := d.MinimumTime
	if minTime <= 0 {
		return nil, errors.New("the project needs a minimum time")
	}
	minAlt := d.MinimumAltitude
	if minAlt < 0 || minAlt >= 90 {
		return nil, errors.New("the minimum altitude must be from 0° to 90°")
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
		out.RuleWeights["Panel Deficit"] = mosaics.PanelDeficitOnWeight
	}
	def := goals.DefaultGoal("")
	plateau := def.PlateauStop
	if d.Goal.PlateauStop != nil {
		plateau = *d.Goal.PlateauStop
	}
	setting := schedcmd.GoalSetting{Kind: schedcmd.GoalKindSNR, SNRGoal: d.Goal.SNR, PlateauStop: plateau}
	if setting.SNRGoal <= 0 {
		setting.SNRGoal = def.SNR
	}
	if d.Goal.Kind == "depth" {
		depth := d.Goal.Depth
		setting.Kind = schedcmd.GoalKindDepth
		setting.DepthGoal = &depth
	}
	if mosaic && d.Mosaic != nil {
		out.Mosaic = &schedcmd.MosaicPlan{Layout: d.Mosaic.Layout, Rotation: d.Mosaic.Rotation, Overlap: d.Mosaic.Overlap, Cols: d.Mosaic.Cols, Rows: d.Mosaic.Rows}
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
		for _, it := range items {
			t := it.tmpl
			nt.Plans = append(nt.Plans, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(t.ID), TemplateName: t.Name, Exposure: it.exposure, Desired: it.desired})
			if !containsFold(filters, t.Filter) {
				filters = append(filters, t.Filter)
				g := setting
				if g.Kind == schedcmd.GoalKindDepth && d.Goal.Depth <= 0 {
					dd := goals.DefaultDepth(frameheader.NormalizeFilter(t.Filter))
					g.DepthGoal = &dd
				}
				out.Goals = append(out.Goals, schedcmd.NewGoal{TargetGUID: nt.GUID, Filter: t.Filter, Setting: g})
			}
		}
		out.Targets = append(out.Targets, nt)
	}
	return out, nil
}

type draftItem struct {
	tmpl     Template
	exposure float64
	desired  int
}

func (s *Snapshot) templateByID(id int) (Template, bool) {
	for _, t := range s.Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

func (s *Snapshot) templateDesired(id int) int {
	var v []int
	for _, p := range s.Projects {
		for _, t := range p.Targets {
			for _, pl := range t.Plans {
				if pl.TemplateID == id && pl.Desired > 0 {
					v = append(v, pl.Desired)
				}
			}
		}
	}
	return medianInt(v)
}

func (s *Snapshot) draftItems(d ProjectDraft) ([]draftItem, error) {
	var out []draftItem
	if d.SetID == "" && len(d.Plans) > 0 {
		for _, pd := range d.Plans {
			t, ok := s.templateByID(pd.TemplateID)
			if !ok {
				return nil, fmt.Errorf("%w: template %d", ErrMissingTemplate, pd.TemplateID)
			}
			if !(pd.Exposure > 0) {
				return nil, fmt.Errorf("%w for %s", ErrNoExposure, t.Name)
			}
			n := d.Desired
			if n <= 0 {
				n = s.templateDesired(t.ID)
			}
			if n <= 0 && d.GoalDriven {
				n = goals.UnmeasurableSubLimit
			}
			if n <= 0 {
				return nil, fmt.Errorf("%w for %s", ErrNoDesired, t.Name)
			}
			out = append(out, draftItem{tmpl: t, exposure: pd.Exposure, desired: n})
		}
		return out, nil
	}
	set, ok := s.SetByID(d.SetID)
	if !ok {
		return nil, ErrUnknownSet
	}
	for _, it := range set.Items {
		t, ok := s.templateByName(it.Template)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrMissingTemplate, it.Template)
		}
		n, err := desiredFor(d.Desired, it)
		if err != nil {
			return nil, err
		}
		out = append(out, draftItem{tmpl: t, exposure: it.Exposure, desired: n})
	}
	return out, nil
}

func desiredFor(requested int, it SetItem) (int, error) {
	if requested > 0 {
		return requested, nil
	}
	if it.Desired > 0 {
		return it.Desired, nil
	}
	return 0, fmt.Errorf("%w for %s", ErrNoDesired, it.Template)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
