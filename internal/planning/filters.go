package planning

import (
	"context"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"gorm.io/gorm"
)

const (
	RoleManaged    = "managed"
	RoleRetired    = "retired"
	GainBroadband  = 0
	GainNarrowband = 100
)

type templateRoleRow struct {
	TemplateID int    `gorm:"column:template_id;primaryKey"`
	ProfileID  string `gorm:"column:profile_id"`
	Filter     string `gorm:"column:filter"`
	Role       string `gorm:"column:role"`
	ChangedAt  string `gorm:"column:changed_at"`
}

func (templateRoleRow) TableName() string { return "ts_template_role" }

// Filter is one filter a new plan can name: its type, the gain its type
// images at, and the template a plan on it uses.
type Filter struct {
	Name       string `json:"name"`
	Narrowband bool   `json:"narrowband"`
	Gain       int    `json:"gain"`
	TemplateID int    `json:"templateId"`
	Managed    bool   `json:"managed"`
}

func loadRoles(ctx context.Context, db *gorm.DB) (map[int]string, error) {
	out := map[int]string{}
	has, err := tables.Has(ctx, db, templateRoleRow{})
	if err != nil || !has {
		return out, err
	}
	var rows []templateRoleRow
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.TemplateID] = r.Role
	}
	return out, nil
}

// IsNarrowband reports whether a filter images at the narrowband gain.
func IsNarrowband(filter string) bool {
	return goals.IsNarrowband(frameheader.NormalizeFilter(strings.TrimSpace(filter)))
}

// FixedGain is the gain every template of the filter's type uses.
func FixedGain(filter string) int {
	if IsNarrowband(filter) {
		return GainNarrowband
	}
	return GainBroadband
}

func sameFilter(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (s *Snapshot) allTemplates() []Template {
	if s.all != nil {
		return s.all
	}
	return s.Templates
}

// AllTemplates is every template, managed ones included.
func (s *Snapshot) AllTemplates() []Template { return s.allTemplates() }

func (s *Snapshot) inProfile(t Template, profile string) bool {
	return profile == "" || s.templateProfiles[t.ID] == profile
}

// HasManaged reports whether the profile has templates the plugin manages,
// which it has only after the template migration.
func (s *Snapshot) HasManaged(profile string) bool {
	return slices.ContainsFunc(s.allTemplates(), func(t Template) bool { return t.Managed && s.inProfile(t, profile) })
}

func better(t, best Template, found bool) bool {
	return !found || t.UsedByPlans > best.UsedByPlans
}

// ResolveFilter is the template a new plan on the filter uses: the managed
// template when there is one; else the most used template of the filter
// that is not retired and has the type's fixed gain; else the most used
// one not retired; else the most used one.
func (s *Snapshot) ResolveFilter(profile, filter string) (Template, bool) {
	var managed, gainMatch, live, last Template
	var hasManaged, hasGain, hasLive, hasLast bool
	gain := FixedGain(filter)
	for _, t := range s.allTemplates() {
		if !sameFilter(t.Filter, filter) || !s.inProfile(t, profile) {
			continue
		}
		switch {
		case t.Managed:
			if !hasManaged {
				managed, hasManaged = t, true
			}
			continue
		case !t.Retired:
			if t.Gain != nil && *t.Gain == gain && better(t, gainMatch, hasGain) {
				gainMatch, hasGain = t, true
			}
			if better(t, live, hasLive) {
				live, hasLive = t, true
			}
		}
		if better(t, last, hasLast) {
			last, hasLast = t, true
		}
	}
	switch {
	case hasManaged:
		return managed, true
	case hasGain:
		return gainMatch, true
	case hasLive:
		return live, true
	}
	return last, hasLast
}

// MostPlans is the most plans any one template of the filter has.
func (s *Snapshot) MostPlans(profile, filter string) int {
	n := 0
	for _, t := range s.allTemplates() {
		if sameFilter(t.Filter, filter) && s.inProfile(t, profile) {
			n = max(n, t.UsedByPlans)
		}
	}
	return n
}

// FilterProfile is the profile filters resolve in: the one most projects use.
func (s *Snapshot) FilterProfile() string {
	return s.Defaults.ProjectSettings.ProfileID
}

func (s *Snapshot) buildFilters(profile string) []Filter {
	var names []string
	for _, t := range s.allTemplates() {
		f := strings.TrimSpace(t.Filter)
		if f == "" || !s.inProfile(t, profile) || slices.ContainsFunc(names, func(n string) bool { return sameFilter(n, f) }) {
			continue
		}
		names = append(names, f)
	}
	out := make([]Filter, 0, len(names))
	for _, n := range names {
		f := Filter{Name: n, Narrowband: IsNarrowband(n), Gain: FixedGain(n)}
		if t, ok := s.ResolveFilter(profile, n); ok {
			f.TemplateID, f.Managed = t.ID, t.Managed
		}
		out = append(out, f)
	}
	return out
}
