package schedcmd

import (
	"encoding/json"
	"fmt"
)

const (
	KindTemplatesMigrate Kind = "templates.migrate"
	KindTemplatesRevert  Kind = "templates.revert"
)

// TemplatesMigratePayload is the payload of templates.migrate and
// templates.revert. A dry run reports what would change and writes nothing.
type TemplatesMigratePayload struct {
	DryRun bool `json:"dry_run"`
}

func decodeTemplatesMigrate(p json.RawMessage) (TemplatesMigratePayload, error) {
	var raw struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := json.Unmarshal(p, &raw); err != nil {
		return TemplatesMigratePayload{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if raw.DryRun == nil {
		return TemplatesMigratePayload{}, fmt.Errorf("%w: dry_run must be true or false", ErrInvalid)
	}
	return TemplatesMigratePayload{DryRun: *raw.DryRun}, nil
}

func templatesObject() ObjectRef {
	return ObjectRef{Entity: entityTemplate, Name: "Exposure templates"}
}

func templatesSpec(kind, undo Kind, describe func(TemplatesMigratePayload) Description) Spec {
	return planningSpec{
		kind:     kind,
		category: CategoryTemplates,
		validate: func(p json.RawMessage) error { _, err := decodeTemplatesMigrate(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeTemplatesMigrate(p)
			if err != nil {
				return Description{}, err
			}
			return describe(v), nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeTemplatesMigrate(p)
			if err != nil {
				return "", nil, err
			}
			if v.DryRun {
				return "", nil, ErrNoInverse
			}
			return undo, marshalRaw(v), nil
		},
	}
}

func dryRun(d Description, v TemplatesMigratePayload) Description {
	if v.DryRun {
		d.Title += " (dry run)"
		d.Note = "Dry run: reports what would change and writes nothing."
	}
	return d
}

func TemplatesMigrateSpec() Spec {
	return templatesSpec(KindTemplatesMigrate, KindTemplatesRevert, func(v TemplatesMigratePayload) Description {
		o := templatesObject()
		return dryRun(Description{
			Title:   "Move every plan to one template per filter",
			Objects: []ObjectRef{o},
			Diffs:   []Diff{{Object: o, Field: "Templates", Before: quote("chosen per plan"), After: quote("one per filter; the old ones retired")}},
			Note:    "Counts and subs stay with each plan. The old templates are kept, not deleted, and Undo moves the plans back.",
		}, v)
	})
}

func TemplatesRevertSpec() Spec {
	return templatesSpec(KindTemplatesRevert, KindTemplatesMigrate, func(v TemplatesMigratePayload) Description {
		o := templatesObject()
		return dryRun(Description{
			Title:   "Move plans back to their old templates",
			Objects: []ObjectRef{o},
			Diffs:   []Diff{{Object: o, Field: "Templates", Before: quote("one per filter"), After: quote("the templates each plan had before")}},
			Note:    "Plans made after the move stay on the per-filter templates.",
		}, v)
	})
}
