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

const KindCatalogMatch Kind = "catalog.match"

var ErrMatchChanged = fmt.Errorf("%w: the match was changed since", ErrInvalid)

type MatchPayload struct {
	Subject     string  `json:"subject"`
	SubjectName string  `json:"subject_name"`
	ObjectID    string  `json:"object_id"`
	ObjectName  string  `json:"object_name"`
	Method      string  `json:"method,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`
	Before      string  `json:"before"`
	After       string  `json:"after"`
}

type MatchSpec struct{}

func MatchingSpecs() []Spec { return []Spec{MatchSpec{}} }

func (MatchSpec) Kind() Kind               { return KindCatalogMatch }
func (MatchSpec) Category() Category       { return CategoryMatching }
func (MatchSpec) Destination() Destination { return DestinationApp }

func decodeMatch(payload json.RawMessage) (MatchPayload, error) {
	var p MatchPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return p, nil
}

func validDecision(d string) bool {
	return d == "" || d == app.XrefConfirmed || d == app.XrefRejected
}

func (MatchSpec) Validate(payload json.RawMessage) error {
	p, err := decodeMatch(payload)
	if err != nil {
		return err
	}
	if p.Subject == "" || p.ObjectID == "" {
		return fmt.Errorf("%w: subject and object_id are required", ErrInvalid)
	}
	if !validDecision(p.Before) || !validDecision(p.After) {
		return fmt.Errorf("%w: a decision is confirmed, rejected or empty", ErrInvalid)
	}
	if p.Before == p.After {
		return fmt.Errorf("%w: the decision is unchanged", ErrInvalid)
	}
	return nil
}

func matchLabel(d, object string) string {
	switch d {
	case app.XrefConfirmed:
		return "matched to " + object
	case app.XrefRejected:
		return "not " + object
	}
	return "undecided"
}

func (MatchSpec) Describe(payload json.RawMessage) (Description, error) {
	p, err := decodeMatch(payload)
	if err != nil {
		return Description{}, err
	}
	name := p.SubjectName
	if name == "" {
		name = p.Subject
	}
	object := p.ObjectName
	if object == "" {
		object = p.ObjectID
	}
	ref := ObjectRef{Entity: "match", Name: name}
	before, after := quote(matchLabel(p.Before, object)), quote(matchLabel(p.After, object))
	return Description{
		Title:   "Name match · " + name,
		Objects: []ObjectRef{ref, {Entity: "catalog", Name: object}},
		Diffs:   []Diff{{Object: ref, Field: "Catalogue object", Before: before, After: after}},
		Note:    "Saved in the app's catalogue links. The scheduler's project name stays as it is.",
	}, nil
}

func (MatchSpec) Inverse(payload json.RawMessage) (Kind, json.RawMessage, error) {
	p, err := decodeMatch(payload)
	if err != nil {
		return "", nil, err
	}
	p.Before, p.After = p.After, p.Before
	out, err := json.Marshal(p)
	return KindCatalogMatch, out, err
}

func (MatchSpec) ApplyApp(ctx context.Context, db *gorm.DB, payload json.RawMessage) error {
	p, err := decodeMatch(payload)
	if err != nil {
		return err
	}
	q := db.WithContext(ctx)
	var current []app.ObjectXref
	if err := q.Where("subject = ? AND object_id = ?", p.Subject, p.ObjectID).Limit(1).Find(&current).Error; err != nil {
		return err
	}
	now := ""
	if len(current) > 0 {
		now = current[0].Decision
	}
	if now != p.Before {
		return fmt.Errorf("%w: it is %q now", ErrMatchChanged, matchLabel(now, p.ObjectID))
	}
	if p.After == "" {
		return q.Where("subject = ? AND object_id = ?", p.Subject, p.ObjectID).Delete(&app.ObjectXref{}).Error
	}
	row := app.ObjectXref{
		Subject: p.Subject, ObjectID: p.ObjectID, SubjectName: p.SubjectName, ObjectName: p.ObjectName,
		Decision: p.After, Method: p.Method, Confidence: p.Confidence, DecidedAt: time.Now().UTC(),
	}
	return q.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subject"}, {Name: "object_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"decision", "decided_at", "subject_name", "object_name", "method", "confidence"}),
	}).Create(&row).Error
}
