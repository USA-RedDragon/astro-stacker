package planning

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

const (
	FilterSetID   = "filters"
	FilterSetName = "Filters"
)

var ErrNoFilters = errors.New("no filters chosen")

func uniqueFilters(filters []string) []string {
	var out []string
	for _, f := range filters {
		f = strings.TrimSpace(f)
		if f != "" && !containsFold(out, f) {
			out = append(out, f)
		}
	}
	return out
}

type filterOff struct {
	name     string
	accepted int
}

func (o filterOff) text() string {
	switch {
	case o.accepted == 1:
		return o.name + " (its 1 kept sub stays)"
	case o.accepted > 1:
		return fmt.Sprintf("%s (its %d kept subs stay)", o.name, o.accepted)
	}
	return o.name
}

func enabledFilters(t Target) string {
	var names []string
	for _, pl := range t.Plans {
		if pl.Enabled && strings.TrimSpace(pl.Filter) != "" && !containsFold(names, pl.Filter) {
			names = append(names, pl.Filter)
		}
	}
	return joinOr(names, "No plans")
}

type filterChange struct {
	at      schedcmd.ApplySetTarget
	adds    []string
	ons     []string
	offs    []filterOff
	missing []string
}

func planOfFilter(t Target, f string) (enabled, disabled *Plan) {
	for i := range t.Plans {
		pl := &t.Plans[i]
		if !sameFilter(pl.Filter, f) {
			continue
		}
		if pl.Enabled && enabled == nil {
			enabled = pl
		} else if !pl.Enabled && disabled == nil {
			disabled = pl
		}
	}
	return enabled, disabled
}

func (s *Snapshot) addFilter(c *filterChange, t Target, profile, f string, newID func() string) error {
	enabled, disabled := planOfFilter(t, f)
	switch {
	case enabled != nil:
		return nil
	case disabled != nil:
		c.at.Enable = append(c.at.Enable, schedcmd.PlanRef{ID: int64(disabled.ID), GUID: disabled.GUID, TemplateName: disabled.Template})
		c.ons = append(c.ons, disabled.Filter)
		return nil
	}
	tm, ok := s.ResolveFilter(profile, f)
	if !ok {
		c.missing = append(c.missing, f)
		return nil
	}
	n := s.templateDesired(tm.ID)
	if n <= 0 && (s.GoalsSetUp() || t.Driven) {
		n = goals.UnmeasurableSubLimit
	}
	if n <= 0 {
		return fmt.Errorf("%w for %s", ErrNoDesired, tm.Filter)
	}
	c.at.Create = append(c.at.Create, schedcmd.NewPlan{GUID: newID(), TemplateID: int64(tm.ID), TemplateName: tm.Name, Filter: s.PlanFilter(tm),
		Exposure: TemplateLength, Desired: n})
	c.adds = append(c.adds, tm.Filter)
	return nil
}

func (c *filterChange) dropOthers(t Target, names []string) {
	for _, pl := range t.Plans {
		if !pl.Enabled || strings.TrimSpace(pl.Filter) == "" || containsFold(names, pl.Filter) {
			continue
		}
		c.at.Disable = append(c.at.Disable, schedcmd.PlanRef{ID: int64(pl.ID), GUID: pl.GUID, TemplateName: pl.Template})
		i := slices.IndexFunc(c.offs, func(o filterOff) bool { return sameFilter(o.name, pl.Filter) })
		if i < 0 {
			c.offs = append(c.offs, filterOff{name: pl.Filter})
			i = len(c.offs) - 1
		}
		c.offs[i].accepted += max(0, pl.Accepted)
	}
}

// DraftFilters is the exposureplan.applyset that leaves each target imaging
// exactly the filters given: a listed filter with no enabled plan gets its
// disabled plan turned on, or a new plan; enabled plans of other filters are
// turned off with their counts kept. Plans are grouped by filter, not
// template.
func (s *Snapshot) DraftFilters(targetIDs []int, filters []string, newID func() string) (ApplySetDraft, error) {
	names := uniqueFilters(filters)
	if len(names) == 0 {
		return ApplySetDraft{}, ErrNoFilters
	}
	if len(targetIDs) == 0 {
		return ApplySetDraft{}, ErrNoTargets
	}
	out := ApplySetDraft{}
	payload := &schedcmd.ApplySetPayload{SetID: FilterSetID, SetName: FilterSetName, Mode: modeReplace}
	gc := &goalCollector{edits: map[int]*schedcmd.GoalEditPayload{}}
	for _, id := range targetIDs {
		t, p, ok := s.target(id)
		if !ok {
			return out, fmt.Errorf("unknown target %d", id)
		}
		profile := p.ProfileID
		if profile == "" {
			profile = s.FilterProfile()
		}
		c := &filterChange{at: schedcmd.ApplySetTarget{TargetID: int64(t.ID), TargetGUID: t.GUID, TargetName: t.Name, Project: p.Name}}
		for _, f := range names {
			if err := s.addFilter(c, t, profile, f, newID); err != nil {
				return out, err
			}
		}
		for _, f := range c.missing {
			if !containsFold(out.Missing, f) {
				out.Missing = append(out.Missing, f)
			}
		}
		c.dropOthers(t, names)
		goalParts := gc.add(s, t, p, append(slices.Clone(c.adds), c.ons...))
		parts := filterEffectParts(c.adds, c.ons, c.offs)
		planChanges := len(parts) > 0
		if len(goalParts) > 0 {
			parts = append(parts, "sets the "+strings.Join(goalParts, "; the "))
		}
		eff := TargetEffect{TargetID: t.ID, Name: t.Name, Project: p.Name, Current: enabledFilters(t), Changes: len(parts) > 0}
		if planChanges {
			payload.Targets = append(payload.Targets, c.at)
		}
		if eff.Changes {
			eff.Effect = joinOr(parts, "")
		} else {
			eff.Effect = "Already has these filters; nothing changes"
		}
		out.Effects = append(out.Effects, eff)
	}
	if len(out.Missing) > 0 {
		return out, fmt.Errorf("%w: no template uses %s", ErrMissingTemplate, strings.Join(out.Missing, ", "))
	}
	if len(payload.Targets) > 0 {
		out.Payload = payload
	}
	out.Goals = gc.payloads()
	return out, nil
}

func filterEffectParts(adds, ons []string, offs []filterOff) []string {
	var parts []string
	if len(adds) > 0 {
		parts = append(parts, "adds "+strings.Join(adds, ", "))
	}
	if len(ons) > 0 {
		parts = append(parts, "turns on "+strings.Join(ons, ", "))
	}
	if len(offs) > 0 {
		texts := make([]string, 0, len(offs))
		for _, o := range offs {
			texts = append(texts, o.text())
		}
		parts = append(parts, "turns off "+strings.Join(texts, ", "))
	}
	return parts
}
