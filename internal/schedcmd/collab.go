package schedcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const KindCollabSwitch Kind = "collab.switch"

const collabSwitchID = 1

type CollabSwitchPayload struct {
	On     bool   `json:"on"`
	Resend string `json:"resend,omitempty"`
}

func decodeCollabSwitch(p json.RawMessage) (CollabSwitchPayload, error) {
	var v CollabSwitchPayload
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(p, &raw); err != nil {
		return v, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, ok := raw["on"]; !ok {
		return v, fmt.Errorf("%w: missing on", ErrInvalid)
	}
	if err := json.Unmarshal(p, &v); err != nil {
		return v, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return v, nil
}

func CollabSwitchState(on bool) string {
	if on {
		return "On, the collab takes the telescope before Target Scheduler"
	}
	return "Paused, Target Scheduler has the telescope"
}

type collabSwitchSpec struct{}

func CollabSwitchSpec() Spec { return collabSwitchSpec{} }

func (collabSwitchSpec) Kind() Kind               { return KindCollabSwitch }
func (collabSwitchSpec) Category() Category       { return CategoryControl }
func (collabSwitchSpec) Destination() Destination { return DestinationApp }

func (collabSwitchSpec) Validate(p json.RawMessage) error {
	_, err := decodeCollabSwitch(p)
	return err
}

func (collabSwitchSpec) Describe(p json.RawMessage) (Description, error) {
	v, err := decodeCollabSwitch(p)
	if err != nil {
		return Description{}, err
	}
	o := ObjectRef{Entity: "collab", Name: "Collab switch"}
	d := Description{Objects: []ObjectRef{o}, Diffs: []Diff{{Object: o, Field: fieldState, After: quote(CollabSwitchState(v.On))}}}
	switch {
	case v.Resend != "" && v.On:
		d.Title = "Send the collab switch again: on"
		d.Note = v.Resend
	case v.Resend != "":
		d.Title = "Send the collab switch again: paused"
		d.Note = v.Resend
	case v.On:
		d.Title = "Turn the collab on"
		d.Note = "Sets the collab constant in NINA's running sequence to 1 and re-plans, so a collaboration takes the telescope at the next safe point."
	default:
		d.Title = "Pause the collab"
		d.Note = "Sets the collab constant in NINA's running sequence to 0, so Target Scheduler takes the telescope back at the next safe point."
	}
	return d, nil
}

func (collabSwitchSpec) Inverse(json.RawMessage) (Kind, json.RawMessage, error) {
	return "", nil, ErrNoInverse
}

func (collabSwitchSpec) ApplyApp(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeCollabSwitch(p)
	if err != nil {
		return err
	}
	if v.Resend != "" {
		return nil
	}
	return SaveCollabSwitch(ctx, db, v.On, AuthorFrom(ctx), time.Now().UTC())
}

func SaveCollabSwitch(ctx context.Context, db *gorm.DB, on bool, by string, at time.Time) error {
	row := app.CollabSwitch{ID: collabSwitchID, Enabled: on, SetBy: by, SetAt: at}
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "set_by", "set_at"}),
	}).Create(&row).Error
}

func LoadCollabSwitch(ctx context.Context, db *gorm.DB) (app.CollabSwitch, bool, error) {
	var rows []app.CollabSwitch
	if err := db.WithContext(ctx).Where("id = ?", collabSwitchID).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return app.CollabSwitch{}, false, err
	}
	return rows[0], true, nil
}
