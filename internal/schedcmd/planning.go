package schedcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	KindGoalEdit          Kind = "goal.edit"
	KindRuleWeightEdit    Kind = "ruleweight.edit"
	KindTemplateBatchEdit Kind = "exposuretemplate.batchedit"
	KindProjectBatchEdit  Kind = "project.batchedit"
	KindPlanBatchEdit     Kind = "exposureplan.batchedit"
	KindTemplateClone     Kind = "exposuretemplate.clone"
	KindTemplateDelete    Kind = "exposuretemplate.delete"
	KindApplySet          Kind = "exposureplan.applyset"
	KindUnapplySet        Kind = "exposureplan.unapplyset"
	KindProjectCreate     Kind = "project.create"
	KindProjectDelete     Kind = "project.delete"
)

const entityTemplate = "exposuretemplate"

const (
	GoalKindSNR   = 0
	GoalKindDepth = 1
)

type GoalSetting struct {
	Kind        int      `json:"kind"`
	SNRGoal     float64  `json:"snr_goal"`
	DepthGoal   *float64 `json:"depth_goal,omitempty"`
	PlateauStop bool     `json:"plateau_stop"`
	Region      string   `json:"region,omitempty"`
}

type GoalChange struct {
	TargetID   int64        `json:"target_id"`
	TargetGUID string       `json:"target_guid"`
	TargetName string       `json:"target_name"`
	Filter     string       `json:"filter"`
	Before     *GoalSetting `json:"before"`
	After      *GoalSetting `json:"after"`
}

type GoalEditPayload struct {
	ProjectID   int64        `json:"project_id"`
	ProjectName string       `json:"project_name"`
	Goals       []GoalChange `json:"goals"`
}

type RuleWeightChange struct {
	Rule   string   `json:"rule"`
	Before *float64 `json:"before"`
	After  *float64 `json:"after"`
}

type RuleWeightEditPayload struct {
	ProjectID   int64              `json:"project_id"`
	ProjectGUID string             `json:"project_guid,omitempty"`
	ProjectName string             `json:"project_name"`
	Changes     []RuleWeightChange `json:"changes"`
}

type TemplateBatchPayload struct {
	Items []EditPayload `json:"items"`
}

type TemplateClonePayload struct {
	SourceID        int64    `json:"source_id"`
	SourceName      string   `json:"source_name"`
	GUID            string   `json:"guid"`
	Name            string   `json:"name"`
	DefaultExposure *float64 `json:"defaultexposure,omitempty"`
	Gain            *int     `json:"gain,omitempty"`
}

type TemplateDeletePayload struct {
	GUID  string                `json:"guid"`
	Name  string                `json:"name"`
	Clone *TemplateClonePayload `json:"clone,omitempty"`
}

type NewPlan struct {
	GUID         string  `json:"guid"`
	TemplateID   int64   `json:"template_id"`
	TemplateName string  `json:"template_name"`
	Exposure     float64 `json:"exposure"`
	Desired      int     `json:"desired"`
}

type PlanRef struct {
	ID           int64  `json:"id"`
	GUID         string `json:"guid,omitempty"`
	TemplateName string `json:"template_name"`
}

type ApplySetTarget struct {
	TargetID   int64     `json:"target_id"`
	TargetGUID string    `json:"target_guid,omitempty"`
	TargetName string    `json:"target_name"`
	Project    string    `json:"project"`
	Create     []NewPlan `json:"create,omitempty"`
	Disable    []PlanRef `json:"disable,omitempty"`
	Enable     []PlanRef `json:"enable,omitempty"`
}

type ApplySetPayload struct {
	SetID   string           `json:"set_id"`
	SetName string           `json:"set_name"`
	Mode    string           `json:"mode"`
	Targets []ApplySetTarget `json:"targets"`
}

type NewProject struct {
	GUID            string  `json:"guid"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	Priority        int     `json:"priority"`
	State           int     `json:"state"`
	MinimumTime     int     `json:"minimumtime"`
	MinimumAltitude float64 `json:"minimumaltitude"`
	IsMosaic        bool    `json:"is_mosaic"`
}

type NewTarget struct {
	GUID     string    `json:"guid"`
	Name     string    `json:"name"`
	RAHours  float64   `json:"ra_hours"`
	Dec      float64   `json:"dec"`
	Rotation float64   `json:"rotation"`
	Plans    []NewPlan `json:"plans"`
}

type NewGoal struct {
	TargetGUID string      `json:"target_guid"`
	Filter     string      `json:"filter"`
	Setting    GoalSetting `json:"setting"`
}

type MosaicPlan struct {
	Layout   string  `json:"layout,omitempty"`
	Rotation float64 `json:"rotation"`
	Overlap  float64 `json:"overlap"`
	Cols     int     `json:"cols,omitempty"`
	Rows     int     `json:"rows,omitempty"`
}

type AppSideEffect interface {
	ApplyAppSide(ctx context.Context, db *gorm.DB, payload json.RawMessage) error
}

type ProjectCreatePayload struct {
	Mosaic      *MosaicPlan        `json:"mosaic,omitempty"`
	Project     NewProject         `json:"project"`
	Targets     []NewTarget        `json:"targets"`
	Goals       []NewGoal          `json:"goals,omitempty"`
	RuleWeights map[string]float64 `json:"rule_weights,omitempty"`
	Catalog     string             `json:"catalog,omitempty"`
	Match       string             `json:"match,omitempty"`
	MatchWith   string             `json:"match_with,omitempty"`
}

func ExposureTemplateEdit() EditSpec {
	return EditSpec{
		KindName:     "exposuretemplate.edit",
		Entity:       entityTemplate,
		EntityLabel:  "Exposure template",
		CategoryName: CategoryTemplates,
		Fields: map[string]FieldSpec{
			"defaultexposure":         {Label: "Default exposure", Format: SuffixFormat(" s")},
			"gain":                    {Label: "Gain"},
			"offset":                  {Label: "Offset"},
			"moonavoidanceseparation": {Label: "Moon avoidance separation", Format: SuffixFormat("°")},
			"moonavoidancewidth":      {Label: "Moon avoidance width", Format: SuffixFormat(" days")},
			"moonavoidanceenabled":    {Label: "Moon avoidance", Format: BoolFormat("On", "Off")},
			"maximumhumidity":         {Label: "Maximum humidity", Format: SuffixFormat("%")},
			"twilightlevel":           {Label: "Twilight", Format: EnumFormat(map[int]string{0: "Nighttime", 1: "Astronomical", 2: "Nautical", 3: "Civil"})},
		},
	}
}

type planningSpec struct {
	kind     Kind
	category Category
	validate func(json.RawMessage) error
	describe func(json.RawMessage) (Description, error)
	inverse  func(json.RawMessage) (Kind, json.RawMessage, error)
	appSide  func(context.Context, *gorm.DB, json.RawMessage) error
}

func (s planningSpec) ApplyAppSide(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	if s.appSide == nil || db == nil {
		return nil
	}
	return s.appSide(ctx, db, p)
}

func (s planningSpec) Kind() Kind                                      { return s.kind }
func (s planningSpec) Category() Category                              { return s.category }
func (s planningSpec) Destination() Destination                        { return DestinationObservatory }
func (s planningSpec) Validate(p json.RawMessage) error                { return s.validate(p) }
func (s planningSpec) Describe(p json.RawMessage) (Description, error) { return s.describe(p) }
func (s planningSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	if s.inverse == nil {
		return "", nil, ErrNoInverse
	}
	return s.inverse(p)
}

func decodeInto[T any](p json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(p, &v); err != nil {
		return v, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return v, nil
}

func marshalRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}

func fnum(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func goalText(g *GoalSetting) string {
	if g == nil {
		return "Scheduler counts"
	}
	var s string
	if g.Kind == GoalKindDepth && g.DepthGoal != nil {
		s = fnum(*g.DepthGoal) + " mag/arcsec²"
	} else {
		s = "SNR " + fnum(g.SNRGoal)
	}
	if g.Region != "" {
		var pts []json.RawMessage
		if json.Unmarshal([]byte(g.Region), &pts) == nil {
			s += fmt.Sprintf(" · %d-point region", len(pts))
		}
	}
	if !g.PlateauStop {
		s += " · no plateau stop"
	}
	return s
}

func validateGoalSetting(g *GoalSetting) error {
	if g == nil {
		return nil
	}
	if g.Kind != GoalKindSNR && g.Kind != GoalKindDepth {
		return fmt.Errorf("%w: goal kind must be 0 or 1", ErrInvalid)
	}
	if g.Kind == GoalKindSNR && (g.SNRGoal < 1 || g.SNRGoal > 1000) {
		return fmt.Errorf("%w: SNR goal must be between 1 and 1000", ErrInvalid)
	}
	if g.Kind == GoalKindDepth && (g.DepthGoal == nil || *g.DepthGoal < 15 || *g.DepthGoal > 32) {
		return fmt.Errorf("%w: depth goal must be between 15 and 32 mag/arcsec²", ErrInvalid)
	}
	if g.Region != "" {
		var pts []struct{ X, Y float64 }
		if err := json.Unmarshal([]byte(g.Region), &pts); err != nil || len(pts) < 3 {
			return fmt.Errorf("%w: region needs at least 3 points", ErrInvalid)
		}
		for _, p := range pts {
			if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
				return fmt.Errorf("%w: region points must be fractions of the frame", ErrInvalid)
			}
		}
	}
	return nil
}

func decodeGoalEdit(p json.RawMessage) (GoalEditPayload, error) {
	v, err := decodeInto[GoalEditPayload](p)
	if err != nil {
		return v, err
	}
	if len(v.Goals) == 0 {
		return v, fmt.Errorf("%w: no goals", ErrInvalid)
	}
	for _, g := range v.Goals {
		if g.TargetGUID == "" || strings.TrimSpace(g.Filter) == "" {
			return v, fmt.Errorf("%w: each goal needs target_guid and filter", ErrInvalid)
		}
		if reflect.DeepEqual(g.Before, g.After) {
			return v, fmt.Errorf("%w: %s %s is unchanged", ErrInvalid, g.TargetName, g.Filter)
		}
		if err := validateGoalSetting(g.Before); err != nil {
			return v, err
		}
		if err := validateGoalSetting(g.After); err != nil {
			return v, err
		}
	}
	return v, nil
}

func GoalEditSpec() Spec {
	return planningSpec{
		kind:     KindGoalEdit,
		category: CategoryGoals,
		validate: func(p json.RawMessage) error { _, err := decodeGoalEdit(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeGoalEdit(p)
			if err != nil {
				return Description{}, err
			}
			d := Description{}
			proj := ObjectRef{Entity: entityProject, ID: v.ProjectID, Name: v.ProjectName}
			if v.ProjectID > 0 {
				d.Objects = append(d.Objects, proj)
			}
			targets := map[string]bool{}
			filters := []string{}
			for _, g := range v.Goals {
				o := ObjectRef{Entity: entityTarget, ID: g.TargetID, GUID: g.TargetGUID, Name: g.TargetName}
				if !targets[g.TargetGUID] {
					targets[g.TargetGUID] = true
					d.Objects = append(d.Objects, o)
				}
				if !containsString(filters, g.Filter) {
					filters = append(filters, g.Filter)
				}
				d.Diffs = append(d.Diffs, Diff{Object: o, Field: g.Filter + " goal", Before: quote(goalText(g.Before)), After: quote(goalText(g.After))})
			}
			name := v.ProjectName
			if name == "" && len(v.Goals) > 0 {
				name = v.Goals[0].TargetName
			}
			d.Title = name + " · goals"
			if len(targets) > 1 {
				d.Note = fmt.Sprintf("Applies to %d targets. The weakest one sets completion.", len(targets))
			}
			return d, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeGoalEdit(p)
			if err != nil {
				return "", nil, err
			}
			for i := range v.Goals {
				v.Goals[i].Before, v.Goals[i].After = v.Goals[i].After, v.Goals[i].Before
			}
			return KindGoalEdit, marshalRaw(v), nil
		},
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func decodeRuleWeights(p json.RawMessage) (RuleWeightEditPayload, error) {
	v, err := decodeInto[RuleWeightEditPayload](p)
	if err != nil {
		return v, err
	}
	if v.ProjectID <= 0 {
		return v, fmt.Errorf("%w: missing project_id", ErrInvalid)
	}
	if len(v.Changes) == 0 {
		return v, fmt.Errorf("%w: no changes", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, c := range v.Changes {
		if c.Rule == "" || seen[c.Rule] {
			return v, fmt.Errorf("%w: rule names must be set and unique", ErrInvalid)
		}
		seen[c.Rule] = true
		for _, w := range []*float64{c.Before, c.After} {
			if w != nil && (*w < 0 || *w > 100) {
				return v, fmt.Errorf("%w: weights run from 0 to 100", ErrInvalid)
			}
		}
		if reflect.DeepEqual(c.Before, c.After) {
			return v, fmt.Errorf("%w: %s is unchanged", ErrInvalid, c.Rule)
		}
	}
	return v, nil
}

func weightText(w *float64) string {
	if w == nil {
		return "not set"
	}
	return fnum(*w)
}

func RuleWeightEditSpec() Spec {
	return planningSpec{
		kind:     KindRuleWeightEdit,
		category: CategoryRules,
		validate: func(p json.RawMessage) error { _, err := decodeRuleWeights(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeRuleWeights(p)
			if err != nil {
				return Description{}, err
			}
			o := ObjectRef{Entity: entityProject, ID: v.ProjectID, GUID: v.ProjectGUID, Name: v.ProjectName}
			d := Description{Title: v.ProjectName + " · rule weights", Objects: []ObjectRef{o}}
			for _, c := range v.Changes {
				d.Diffs = append(d.Diffs, Diff{Object: o, Field: c.Rule + " weight", Before: quote(weightText(c.Before)), After: quote(weightText(c.After))})
			}
			return d, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeRuleWeights(p)
			if err != nil {
				return "", nil, err
			}
			for i := range v.Changes {
				v.Changes[i].Before, v.Changes[i].After = v.Changes[i].After, v.Changes[i].Before
			}
			return KindRuleWeightEdit, marshalRaw(v), nil
		},
	}
}

func decodeBatch(p json.RawMessage, spec EditSpec) (TemplateBatchPayload, error) {
	v, err := decodeInto[TemplateBatchPayload](p)
	if err != nil {
		return v, err
	}
	if len(v.Items) == 0 {
		return v, fmt.Errorf("%w: nothing to edit", ErrInvalid)
	}
	for _, it := range v.Items {
		it.Entity = spec.Entity
		if err := spec.Validate(marshalRaw(it)); err != nil {
			return v, err
		}
	}
	return v, nil
}

func TemplateBatchEditSpec() Spec {
	return batchSpec(KindTemplateBatchEdit, ExposureTemplateEdit(), "template", "templates")
}

func ProjectBatchEditSpec() Spec {
	return batchSpec(KindProjectBatchEdit, ProjectEdit(), "project", "projects")
}

func PlanBatchEditSpec() Spec {
	return batchSpec(KindPlanBatchEdit, ExposurePlanEdit(), "plan", "plans")
}

func batchSpec(kind Kind, spec EditSpec, one, many string) Spec {
	return planningSpec{
		kind:     kind,
		category: spec.CategoryName,
		validate: func(p json.RawMessage) error { _, err := decodeBatch(p, spec); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeBatch(p, spec)
			if err != nil {
				return Description{}, err
			}
			d := Description{}
			labels := []string{}
			for _, it := range v.Items {
				it.Entity = spec.Entity
				sub, err := spec.Describe(marshalRaw(it))
				if err != nil {
					return Description{}, err
				}
				d.Objects = append(d.Objects, sub.Objects...)
				d.Diffs = append(d.Diffs, sub.Diffs...)
				for _, c := range it.Changes {
					l := spec.Fields[c.Field].Label
					if !containsString(labels, l) {
						labels = append(labels, l)
					}
				}
			}
			sort.Strings(labels)
			d.Title = strings.Join(labels, ", ") + " · " + plural(len(v.Items), one, many)
			return d, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeBatch(p, spec)
			if err != nil {
				return "", nil, err
			}
			for i, it := range v.Items {
				for j, c := range it.Changes {
					v.Items[i].Changes[j] = FieldChange{Field: c.Field, Before: c.After, After: c.Before}
				}
			}
			return kind, marshalRaw(v), nil
		},
	}
}

func decodeClone(p json.RawMessage) (TemplateClonePayload, error) {
	v, err := decodeInto[TemplateClonePayload](p)
	if err != nil {
		return v, err
	}
	if v.SourceID <= 0 || v.GUID == "" || strings.TrimSpace(v.Name) == "" {
		return v, fmt.Errorf("%w: clone needs source_id, guid and name", ErrInvalid)
	}
	return v, nil
}

func TemplateCloneSpec() Spec {
	return planningSpec{
		kind:     KindTemplateClone,
		category: CategoryTemplates,
		validate: func(p json.RawMessage) error { _, err := decodeClone(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeClone(p)
			if err != nil {
				return Description{}, err
			}
			o := ObjectRef{Entity: entityTemplate, GUID: v.GUID, Name: v.Name}
			return Description{
				Title:   v.Name + " · copied from " + v.SourceName,
				Objects: []ObjectRef{o, {Entity: entityTemplate, ID: v.SourceID, Name: v.SourceName}},
				Diffs:   []Diff{{Object: o, Field: "Template", Before: quote("none"), After: quote("copy of " + v.SourceName)}},
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeClone(p)
			if err != nil {
				return "", nil, err
			}
			return KindTemplateDelete, marshalRaw(TemplateDeletePayload{GUID: v.GUID, Name: v.Name, Clone: &v}), nil
		},
	}
}

func TemplateDeleteSpec() Spec {
	decode := func(p json.RawMessage) (TemplateDeletePayload, error) {
		v, err := decodeInto[TemplateDeletePayload](p)
		if err != nil {
			return v, err
		}
		if v.GUID == "" {
			return v, fmt.Errorf("%w: missing guid", ErrInvalid)
		}
		return v, nil
	}
	return planningSpec{
		kind:     KindTemplateDelete,
		category: CategoryTemplates,
		validate: func(p json.RawMessage) error { _, err := decode(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decode(p)
			if err != nil {
				return Description{}, err
			}
			o := ObjectRef{Entity: entityTemplate, GUID: v.GUID, Name: v.Name}
			return Description{
				Title:   "Remove template " + v.Name,
				Objects: []ObjectRef{o},
				Diffs:   []Diff{{Object: o, Field: "Template", Before: quote(v.Name), After: quote("removed")}},
				Note:    "Only a template no exposure plan uses can be removed.",
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decode(p)
			if err != nil {
				return "", nil, err
			}
			if v.Clone == nil {
				return "", nil, ErrNoInverse
			}
			return KindTemplateClone, marshalRaw(v.Clone), nil
		},
	}
}

func decodeApplySet(p json.RawMessage) (ApplySetPayload, error) {
	v, err := decodeInto[ApplySetPayload](p)
	if err != nil {
		return v, err
	}
	if v.Mode != "add" && v.Mode != "replace" {
		return v, fmt.Errorf("%w: mode must be add or replace", ErrInvalid)
	}
	if len(v.Targets) == 0 {
		return v, fmt.Errorf("%w: no targets", ErrInvalid)
	}
	changed := false
	for _, t := range v.Targets {
		if t.TargetID <= 0 {
			return v, fmt.Errorf("%w: missing target_id", ErrInvalid)
		}
		for _, c := range t.Create {
			if c.GUID == "" || c.TemplateID <= 0 || c.Desired < 0 {
				return v, fmt.Errorf("%w: a new plan needs guid, template_id and desired", ErrInvalid)
			}
		}
		if len(t.Create)+len(t.Disable)+len(t.Enable) > 0 {
			changed = true
		}
	}
	if !changed {
		return v, fmt.Errorf("%w: nothing to change", ErrInvalid)
	}
	return v, nil
}

func applySetDiffs(v ApplySetPayload, undo bool) []Diff {
	var out []Diff
	for _, t := range v.Targets {
		o := ObjectRef{Entity: entityTarget, ID: t.TargetID, GUID: t.TargetGUID, Name: t.TargetName}
		var add, off, on []string
		for _, c := range t.Create {
			add = append(add, c.TemplateName)
		}
		for _, c := range t.Disable {
			off = append(off, c.TemplateName)
		}
		for _, c := range t.Enable {
			on = append(on, c.TemplateName)
		}
		parts := []string{}
		if len(add) > 0 {
			verb := "add "
			if undo {
				verb = "remove "
			}
			parts = append(parts, verb+strings.Join(add, ", "))
		}
		if len(on) > 0 {
			verb := "turn on "
			if undo {
				verb = "turn off "
			}
			parts = append(parts, verb+strings.Join(on, ", "))
		}
		if len(off) > 0 {
			verb := "turn off "
			if undo {
				verb = "turn on "
			}
			parts = append(parts, verb+strings.Join(off, ", "))
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, Diff{Object: o, Field: "Exposure plans", Before: quote("as before"), After: quote(strings.Join(parts, "; "))})
	}
	return out
}

func applySetObjects(v ApplySetPayload) []ObjectRef {
	out := make([]ObjectRef, 0, len(v.Targets))
	for _, t := range v.Targets {
		out = append(out, ObjectRef{Entity: entityTarget, ID: t.TargetID, GUID: t.TargetGUID, Name: t.TargetName})
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func ApplySetSpec() Spec {
	return planningSpec{
		kind:     KindApplySet,
		category: CategoryPlans,
		validate: func(p json.RawMessage) error { _, err := decodeApplySet(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeApplySet(p)
			if err != nil {
				return Description{}, err
			}
			note := "Existing plans are kept; missing ones are added."
			if v.Mode == "replace" {
				note = "Plans outside the set are turned off, not deleted, so their counts are kept."
			}
			return Description{
				Title:   v.SetName + " · " + plural(len(v.Targets), "target", "targets"),
				Objects: applySetObjects(v),
				Diffs:   applySetDiffs(v, false),
				Note:    note,
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeApplySet(p)
			if err != nil {
				return "", nil, err
			}
			return KindUnapplySet, marshalRaw(v), nil
		},
	}
}

func UnapplySetSpec() Spec {
	return planningSpec{
		kind:     KindUnapplySet,
		category: CategoryPlans,
		validate: func(p json.RawMessage) error { _, err := decodeApplySet(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeApplySet(p)
			if err != nil {
				return Description{}, err
			}
			return Description{
				Title:   "Take back " + v.SetName + " · " + plural(len(v.Targets), "target", "targets"),
				Objects: applySetObjects(v),
				Diffs:   applySetDiffs(v, true),
				Note:    "Added plans that already have subs are turned off instead of removed.",
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeApplySet(p)
			if err != nil {
				return "", nil, err
			}
			return KindApplySet, marshalRaw(v), nil
		},
	}
}

func decodeProjectCreate(p json.RawMessage) (ProjectCreatePayload, error) {
	v, err := decodeInto[ProjectCreatePayload](p)
	if err != nil {
		return v, err
	}
	if v.Project.GUID == "" || strings.TrimSpace(v.Project.Name) == "" {
		return v, fmt.Errorf("%w: the project needs a guid and a name", ErrInvalid)
	}
	if v.Project.Priority < 0 || v.Project.Priority > 2 || v.Project.State < 0 || v.Project.State > 3 {
		return v, fmt.Errorf("%w: priority or state out of range", ErrInvalid)
	}
	if len(v.Targets) == 0 {
		return v, fmt.Errorf("%w: at least one target", ErrInvalid)
	}
	guids := map[string]bool{v.Project.GUID: true}
	for _, t := range v.Targets {
		if t.GUID == "" || strings.TrimSpace(t.Name) == "" || guids[t.GUID] {
			return v, fmt.Errorf("%w: each target needs a unique guid and a name", ErrInvalid)
		}
		guids[t.GUID] = true
		if t.RAHours < 0 || t.RAHours >= 24 || t.Dec < -90 || t.Dec > 90 {
			return v, fmt.Errorf("%w: %s coordinates out of range", ErrInvalid, t.Name)
		}
		for _, pl := range t.Plans {
			if pl.GUID == "" || pl.TemplateID <= 0 || guids[pl.GUID] {
				return v, fmt.Errorf("%w: each plan needs a unique guid and a template", ErrInvalid)
			}
			guids[pl.GUID] = true
		}
	}
	for _, g := range v.Goals {
		if !guids[g.TargetGUID] || g.Filter == "" {
			return v, fmt.Errorf("%w: a goal names an unknown target", ErrInvalid)
		}
		s := g.Setting
		if err := validateGoalSetting(&s); err != nil {
			return v, err
		}
	}
	return v, nil
}

func projectCreateDescription(v ProjectCreatePayload, deleting bool) Description {
	o := ObjectRef{Entity: entityProject, GUID: v.Project.GUID, Name: v.Project.Name}
	plans := 0
	for _, t := range v.Targets {
		plans += len(t.Plans)
	}
	summary := ProjectPriorities()[v.Project.Priority]
	if v.Project.IsMosaic {
		summary += " · " + plural(len(v.Targets), "panel", "panels")
	}
	summary += " · " + plural(plans, "plan", "plans")
	d := Description{Objects: []ObjectRef{o}}
	if deleting {
		d.Title = v.Project.Name + " · project removed"
		d.Diffs = []Diff{{Object: o, Field: labelProject, Before: quote(summary), After: quote("none")}}
		d.Note = "Refused if any of its targets already has subs."
		return d
	}
	d.Title = v.Project.Name + " · project created"
	d.Diffs = []Diff{{Object: o, Field: labelProject, Before: quote("none"), After: quote(summary)}}
	notes := []string{}
	if v.Match == "separate" && v.MatchWith != "" {
		notes = append(notes, "Kept separate from "+v.MatchWith+" after the name-match review.")
	}
	if len(v.Goals) > 0 {
		g := v.Goals[0].Setting
		notes = append(notes, "Goal: "+goalText(&g)+" per filter.")
	}
	d.Note = strings.Join(notes, " ")
	return d
}

func writeWizardPanels(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeProjectCreate(p)
	if err != nil {
		return err
	}
	if !v.Project.IsMosaic || len(v.Targets) < 2 {
		return nil
	}
	targets := make([]mosaics.TSTarget, len(v.Targets))
	for i, t := range v.Targets {
		targets[i] = mosaics.TSTarget{GUID: t.GUID, Name: t.Name, RA: t.RAHours * 15, Dec: t.Dec, Rotation: t.Rotation, Active: true}
	}
	rig := mosaics.DefaultRig()
	now := time.Now().UTC()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_guid = ? AND source = ?", v.Project.GUID, app.MosaicSourceWizard).Delete(&app.MosaicPanel{}).Error; err != nil {
			return err
		}
		for _, panel := range mosaics.PlannedPanels(targets, rig) {
			row, err := mosaicstore.PanelRow(v.Project.GUID, v.Project.Name, panel, rig, app.MosaicSourceWizard, now)
			if err != nil {
				return err
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func removeWizardPanels(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeProjectCreate(p)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Where("project_guid = ? AND source = ?", v.Project.GUID, app.MosaicSourceWizard).Delete(&app.MosaicPanel{}).Error
}

func ProjectCreateSpec() Spec {
	return planningSpec{
		kind:     KindProjectCreate,
		category: CategoryCreated,
		appSide:  writeWizardPanels,
		validate: func(p json.RawMessage) error { _, err := decodeProjectCreate(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeProjectCreate(p)
			if err != nil {
				return Description{}, err
			}
			return projectCreateDescription(v, false), nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			if _, err := decodeProjectCreate(p); err != nil {
				return "", nil, err
			}
			return KindProjectDelete, p, nil
		},
	}
}

func ProjectDeleteSpec() Spec {
	return planningSpec{
		kind:     KindProjectDelete,
		category: CategoryCreated,
		appSide:  removeWizardPanels,
		validate: func(p json.RawMessage) error { _, err := decodeProjectCreate(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeProjectCreate(p)
			if err != nil {
				return Description{}, err
			}
			return projectCreateDescription(v, true), nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			if _, err := decodeProjectCreate(p); err != nil {
				return "", nil, err
			}
			return KindProjectCreate, p, nil
		},
	}
}

func PlanningSpecs() []Spec {
	return []Spec{
		GoalEditSpec(),
		RuleWeightEditSpec(),
		ExposureTemplateEdit(),
		TemplateBatchEditSpec(),
		ProjectBatchEditSpec(),
		PlanBatchEditSpec(),
		TemplateCloneSpec(),
		TemplateDeleteSpec(),
		ApplySetSpec(),
		UnapplySetSpec(),
		ProjectCreateSpec(),
		ProjectDeleteSpec(),
	}
}
