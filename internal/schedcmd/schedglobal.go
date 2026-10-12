package schedcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"gorm.io/gorm"
)

const KindSchedulingGlobal Kind = "scheduling.global"

const entityGlobalScheduling = "scheduling"

// HardMinAltitude is the lowest minimum altitude a project gets without
// the observatory horizon; a global maximum must be above it.
const HardMinAltitude = 15.0

// GlobalSchedulingPayload saves the global scheduling settings. Before is
// the saved set the change expects, nil when nothing is saved; After nil
// removes the saved set so the defaults apply.
type GlobalSchedulingPayload struct {
	Before *schedglobal.Settings `json:"before"`
	After  *schedglobal.Settings `json:"after"`
}

type globalSchedulingSpec struct{}

func GlobalSchedulingSpec() Spec { return globalSchedulingSpec{} }

func (globalSchedulingSpec) Kind() Kind               { return KindSchedulingGlobal }
func (globalSchedulingSpec) Category() Category       { return CategoryRules }
func (globalSchedulingSpec) Destination() Destination { return DestinationApp }

// CheckGlobalScheduling checks every value of a global set.
func CheckGlobalScheduling(s schedglobal.Settings) error {
	for _, err := range []error{
		CheckDitherEvery(s.DitherEvery), CheckFilterSwitchFrequency(s.FilterSwitchFrequency), CheckFlatsHandling(s.FlatsHandling),
		CheckMeridianWindow(s.MeridianWindow), CheckMaximumAltitude(s.MaximumAltitude),
	} {
		if err != nil {
			return err
		}
	}
	if s.MaximumAltitude > 0 && s.MaximumAltitude <= HardMinAltitude {
		return fmt.Errorf("%w: maximum altitude must be 0 for off or above %g°, the lowest minimum altitude", ErrInvalid, HardMinAltitude)
	}
	return nil
}

func decodeGlobalScheduling(p json.RawMessage) (GlobalSchedulingPayload, error) {
	v, err := decodeInto[GlobalSchedulingPayload](p)
	if err != nil {
		return v, err
	}
	for _, s := range []*schedglobal.Settings{v.Before, v.After} {
		if s == nil {
			continue
		}
		if err := CheckGlobalScheduling(*s); err != nil {
			return v, err
		}
	}
	if v.Before == nil && v.After == nil || v.Before != nil && v.After != nil && *v.Before == *v.After {
		return v, fmt.Errorf("%w: no changes", ErrInvalid)
	}
	return v, nil
}

func (globalSchedulingSpec) Validate(p json.RawMessage) error {
	_, err := decodeGlobalScheduling(p)
	return err
}

func globalSchedulingFields(s *schedglobal.Settings) map[string]json.RawMessage {
	if s == nil {
		d := schedglobal.Defaults()
		s = &d
	}
	return map[string]json.RawMessage{
		FieldDitherEvery:           marshalRaw(s.DitherEvery),
		FieldFilterSwitchFrequency: marshalRaw(s.FilterSwitchFrequency),
		FieldFlatsHandling:         marshalRaw(s.FlatsHandling),
		FieldSmartExposureOrder:    marshalRaw(s.SmartExposureOrder),
		FieldMeridianWindow:        marshalRaw(s.MeridianWindow),
		FieldMaximumAltitude:       marshalRaw(s.MaximumAltitude),
		FieldEnableGrader:          marshalRaw(s.EnableGrader),
	}
}

func (globalSchedulingSpec) Describe(p json.RawMessage) (Description, error) {
	v, err := decodeGlobalScheduling(p)
	if err != nil {
		return Description{}, err
	}
	o := ObjectRef{Entity: entityGlobalScheduling, Name: "Global scheduling settings"}
	d := Description{Title: "Global scheduling settings", Objects: []ObjectRef{o},
		Note: "Saved in the app. The stacker then copies them to every project that differs."}
	fields := projectSettingFields()
	before, after := globalSchedulingFields(v.Before), globalSchedulingFields(v.After)
	for _, k := range []string{FieldDitherEvery, FieldFilterSwitchFrequency, FieldFlatsHandling, FieldSmartExposureOrder, FieldMeridianWindow, FieldMaximumAltitude, FieldEnableGrader} {
		if string(before[k]) == string(after[k]) {
			continue
		}
		f := fields[k]
		d.Diffs = append(d.Diffs, Diff{Object: o, Field: f.Label, Before: formatted(f, before[k]), After: formatted(f, after[k])})
	}
	return d, nil
}

func (globalSchedulingSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	v, err := decodeGlobalScheduling(p)
	if err != nil {
		return "", nil, err
	}
	v.Before, v.After = v.After, v.Before
	return KindSchedulingGlobal, marshalRaw(v), nil
}

func (globalSchedulingSpec) ApplyApp(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := decodeGlobalScheduling(p)
	if err != nil {
		return err
	}
	if err := schedglobal.Apply(ctx, db, v.Before, v.After, AuthorFrom(ctx), time.Now().UTC()); err != nil {
		if errors.Is(err, schedglobal.ErrStale) {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return err
	}
	return nil
}
