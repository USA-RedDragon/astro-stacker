package schedcmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicstore"
	"gorm.io/gorm"
)

const KindMosaicAdopt Kind = "mosaic.adopt"

const entityAdoption = "adoption"

type AdoptionDecision struct {
	ID      int    `json:"id"`
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type MosaicAdoptPayload struct {
	Decisions []AdoptionDecision `json:"decisions"`
}

type mosaicAdoptSpec struct{}

func MosaicAdoptSpec() Spec { return mosaicAdoptSpec{} }

func (mosaicAdoptSpec) Kind() Kind               { return KindMosaicAdopt }
func (mosaicAdoptSpec) Category() Category       { return CategoryAdoption }
func (mosaicAdoptSpec) Destination() Destination { return DestinationApp }

func decodeMosaicAdopt(p json.RawMessage) (MosaicAdoptPayload, error) {
	v, err := decodeInto[MosaicAdoptPayload](p)
	if err != nil {
		return v, err
	}
	if len(v.Decisions) == 0 {
		return v, fmt.Errorf("%w: no decisions", ErrInvalid)
	}
	seen := map[int]bool{}
	for _, d := range v.Decisions {
		if d.ID <= 0 || seen[d.ID] || !mosaicstore.ValidStatus(d.Before) || !mosaicstore.ValidStatus(d.After) || d.Before == d.After {
			return v, fmt.Errorf("%w: each decision needs an id and two different statuses", ErrInvalid)
		}
		seen[d.ID] = true
	}
	return v, nil
}

func (mosaicAdoptSpec) Validate(p json.RawMessage) error {
	_, err := decodeMosaicAdopt(p)
	return err
}

func statusWord(s string) string {
	switch s {
	case "accepted":
		return "accepted"
	case "rejected":
		return "rejected"
	case "auto":
		return "adopted automatically"
	}
	return "undecided"
}

func (mosaicAdoptSpec) Describe(p json.RawMessage) (Description, error) {
	v, err := decodeMosaicAdopt(p)
	if err != nil {
		return Description{}, err
	}
	d := Description{Title: "Mosaic adoption · " + plural(len(v.Decisions), "decision", "decisions"),
		Note: "Additive: side tables only. No scheduler row is rewritten."}
	for _, x := range v.Decisions {
		o := ObjectRef{Entity: entityAdoption, ID: int64(x.ID), Name: x.Title}
		d.Objects = append(d.Objects, o)
		d.Diffs = append(d.Diffs, Diff{Object: o, Field: "Adoption", Before: quote(statusWord(x.Before)), After: quote(statusWord(x.After))})
	}
	return d, nil
}

func (mosaicAdoptSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	v, err := decodeMosaicAdopt(p)
	if err != nil {
		return "", nil, err
	}
	inv := MosaicAdoptPayload{Decisions: make([]AdoptionDecision, 0, len(v.Decisions))}
	for i := len(v.Decisions) - 1; i >= 0; i-- {
		x := v.Decisions[i]
		x.Before, x.After = x.After, x.Before
		inv.Decisions = append(inv.Decisions, x)
	}
	return KindMosaicAdopt, marshalRaw(inv), nil
}

func (mosaicAdoptSpec) ApplyApp(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeMosaicAdopt(p)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, x := range v.Decisions {
			if _, err := mosaicstore.SetStatus(ctx, tx, x.ID, x.Before, x.After, "web", mosaics.DefaultRig()); err != nil {
				return err
			}
		}
		return nil
	})
}
