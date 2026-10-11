package schedcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ruleweights"
	"gorm.io/gorm"
)

const KindRuleWeightGlobal Kind = "ruleweight.global"

const entityGlobalWeights = "ruleweights"

type GlobalWeightsPayload struct {
	Changes []ruleweights.Change `json:"changes"`
}

type globalWeightsSpec struct{}

func GlobalWeightsSpec() Spec { return globalWeightsSpec{} }

func (globalWeightsSpec) Kind() Kind               { return KindRuleWeightGlobal }
func (globalWeightsSpec) Category() Category       { return CategoryRules }
func (globalWeightsSpec) Destination() Destination { return DestinationApp }

func decodeGlobalWeights(p json.RawMessage) (GlobalWeightsPayload, error) {
	v, err := decodeInto[GlobalWeightsPayload](p)
	if err != nil {
		return v, err
	}
	if err := ruleweights.Validate(v.Changes); err != nil {
		return v, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return v, nil
}

func (globalWeightsSpec) Validate(p json.RawMessage) error {
	_, err := decodeGlobalWeights(p)
	return err
}

func savedText(w *float64) string {
	if w == nil {
		return "not saved"
	}
	return fnum(*w)
}

func (globalWeightsSpec) Describe(p json.RawMessage) (Description, error) {
	v, err := decodeGlobalWeights(p)
	if err != nil {
		return Description{}, err
	}
	o := ObjectRef{Entity: entityGlobalWeights, Name: "Global rule weights"}
	d := Description{Title: "Global rule weights", Objects: []ObjectRef{o},
		Note: "Saved in the app. Projects change only when their rule weights are updated to the global set."}
	for _, c := range v.Changes {
		d.Diffs = append(d.Diffs, Diff{Object: o, Field: c.Rule + " weight", Before: quote(savedText(c.Before)), After: quote(savedText(c.After))})
	}
	return d, nil
}

func (globalWeightsSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	v, err := decodeGlobalWeights(p)
	if err != nil {
		return "", nil, err
	}
	for i := range v.Changes {
		v.Changes[i].Before, v.Changes[i].After = v.Changes[i].After, v.Changes[i].Before
	}
	return KindRuleWeightGlobal, marshalRaw(v), nil
}

func (globalWeightsSpec) ApplyApp(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeGlobalWeights(p)
	if err != nil {
		return err
	}
	if err := ruleweights.Apply(ctx, db, v.Changes, AuthorFrom(ctx), time.Now().UTC()); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}
