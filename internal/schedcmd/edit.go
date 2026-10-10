package schedcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type FieldChange struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

type EditPayload struct {
	Entity  string        `json:"entity"`
	ID      int64         `json:"id"`
	GUID    string        `json:"guid,omitempty"`
	Name    string        `json:"name"`
	Parent  string        `json:"parent,omitempty"`
	Changes []FieldChange `json:"changes"`
}

type FieldSpec struct {
	Label  string
	Format func(json.RawMessage) string
}

type EditSpec struct {
	KindName     Kind
	Entity       string
	EntityLabel  string
	CategoryName Category
	Fields       map[string]FieldSpec
}

func (s EditSpec) Kind() Kind               { return s.KindName }
func (s EditSpec) Category() Category       { return s.CategoryName }
func (s EditSpec) Destination() Destination { return DestinationObservatory }

func (s EditSpec) decode(payload json.RawMessage) (EditPayload, error) {
	var p EditPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if p.Entity == "" {
		p.Entity = s.Entity
	}
	return p, nil
}

func (s EditSpec) Validate(payload json.RawMessage) error {
	p, err := s.decode(payload)
	if err != nil {
		return err
	}
	if p.Entity != s.Entity {
		return fmt.Errorf("%w: entity %q, want %q", ErrInvalid, p.Entity, s.Entity)
	}
	if p.ID <= 0 {
		return fmt.Errorf("%w: missing id", ErrInvalid)
	}
	if len(p.Changes) == 0 {
		return fmt.Errorf("%w: no changes", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, c := range p.Changes {
		if _, ok := s.Fields[c.Field]; !ok {
			return fmt.Errorf("%w: %s.%s is not editable", ErrInvalid, s.Entity, c.Field)
		}
		if seen[c.Field] {
			return fmt.Errorf("%w: %s changed twice", ErrInvalid, c.Field)
		}
		seen[c.Field] = true
		if len(c.After) == 0 || len(c.Before) == 0 {
			return fmt.Errorf("%w: %s needs before and after", ErrInvalid, c.Field)
		}
		if bytes.Equal(bytes.TrimSpace(c.Before), bytes.TrimSpace(c.After)) {
			return fmt.Errorf("%w: %s is unchanged", ErrInvalid, c.Field)
		}
	}
	return nil
}

func (s EditSpec) Describe(payload json.RawMessage) (Description, error) {
	p, err := s.decode(payload)
	if err != nil {
		return Description{}, err
	}
	obj := ObjectRef{Entity: p.Entity, ID: p.ID, GUID: p.GUID, Name: p.Name}
	d := Description{Objects: []ObjectRef{obj}}
	parts := make([]string, 0, len(p.Changes))
	for _, c := range p.Changes {
		f := s.Fields[c.Field]
		label := f.Label
		if label == "" {
			label = c.Field
		}
		parts = append(parts, label)
		d.Diffs = append(d.Diffs, Diff{Object: obj, Field: label, Before: formatted(f, c.Before), After: formatted(f, c.After)})
	}
	name := p.Name
	if p.Parent != "" {
		name = p.Parent + " / " + p.Name
	}
	d.Title = name + " · " + strings.Join(parts, ", ")
	return d, nil
}

func formatted(f FieldSpec, v json.RawMessage) json.RawMessage {
	if f.Format == nil {
		return v
	}
	out, err := json.Marshal(f.Format(v))
	if err != nil {
		return v
	}
	return out
}

func (s EditSpec) Inverse(payload json.RawMessage) (Kind, json.RawMessage, error) {
	p, err := s.decode(payload)
	if err != nil {
		return "", nil, err
	}
	inv := p
	inv.Changes = make([]FieldChange, len(p.Changes))
	for i, c := range p.Changes {
		inv.Changes[i] = FieldChange{Field: c.Field, Before: c.After, After: c.Before}
	}
	out, err := json.Marshal(inv)
	return s.KindName, out, err
}

func EnumFormat(names map[int]string) func(json.RawMessage) string {
	return func(v json.RawMessage) string {
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			if s, ok := names[n]; ok {
				return s
			}
		}
		return string(v)
	}
}

func BoolFormat(on, off string) func(json.RawMessage) string {
	return func(v json.RawMessage) string {
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			if b {
				return on
			}
			return off
		}
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			if n != 0 {
				return on
			}
			return off
		}
		return string(v)
	}
}

func SuffixFormat(suffix string) func(json.RawMessage) string {
	return func(v json.RawMessage) string { return string(v) + suffix }
}
