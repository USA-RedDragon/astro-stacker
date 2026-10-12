package schedcmd

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

const (
	KindTargetFraming Kind = "target.framing"
	// MaxOffsetFrac is the furthest from the frame centre, in long sides,
	// the plugin lets a body sit.
	MaxOffsetFrac = 0.5
)

// Framing is how a moving body sits in the frame: RotationDeg is the
// position angle, degrees east of north, of the frame's long side, pointing
// to its positive end, and OffsetFrac where the body sits from the frame
// centre along the long side, as a fraction of it, positive toward the end
// at RotationDeg. A comet framed along its tail has RotationDeg at the
// anti-Sun angle and a negative OffsetFrac, the head toward the sunward end.
type Framing struct {
	RotationDeg *float64 `json:"rotation_deg"`
	OffsetFrac  *float64 `json:"offset_frac"`
}

func checkFraming(rotation, offset *float64) error {
	if rotation == nil && offset == nil {
		return nil
	}
	if rotation == nil || offset == nil {
		return fmt.Errorf("%w: rotation_deg and offset_frac go together", ErrInvalid)
	}
	if math.IsNaN(*rotation) || *rotation < 0 || *rotation >= 360 {
		return fmt.Errorf("%w: rotation_deg runs from 0 to under 360", ErrInvalid)
	}
	if math.IsNaN(*offset) || math.Abs(*offset) > MaxOffsetFrac {
		return fmt.Errorf("%w: offset_frac runs from -%g to %g", ErrInvalid, MaxOffsetFrac, MaxOffsetFrac)
	}
	return nil
}

func (f *Framing) Validate() error {
	if f == nil {
		return nil
	}
	if f.RotationDeg == nil && f.OffsetFrac == nil {
		return fmt.Errorf("%w: framing needs rotation_deg and offset_frac, or null to clear it", ErrInvalid)
	}
	return checkFraming(f.RotationDeg, f.OffsetFrac)
}

func (f *Framing) Text() string {
	if f == nil || f.RotationDeg == nil || f.OffsetFrac == nil {
		return "none"
	}
	deg := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "°" }
	s := "long side at " + deg(*f.RotationDeg) + ", body "
	if *f.OffsetFrac == 0 {
		return s + "centred"
	}
	toward := *f.RotationDeg
	if *f.OffsetFrac < 0 {
		toward = math.Mod(toward+180, 360)
	}
	return s + strconv.FormatFloat(math.Abs(*f.OffsetFrac)*100, 'f', -1, 64) + "% of the long side from the centre toward " + deg(toward)
}

type TargetFramingPayload struct {
	TargetID   int64    `json:"target_id"`
	TargetGUID string   `json:"target_guid"`
	TargetName string   `json:"target_name"`
	Framing    *Framing `json:"framing"`
	Before     *Framing `json:"before,omitempty"`
}

func decodeTargetFraming(p json.RawMessage) (TargetFramingPayload, error) {
	v, err := decodeInto[TargetFramingPayload](p)
	if err != nil {
		return v, err
	}
	if v.TargetGUID == "" {
		return v, fmt.Errorf("%w: missing target_guid", ErrInvalid)
	}
	for _, f := range []*Framing{v.Framing, v.Before} {
		if err := f.Validate(); err != nil {
			return v, err
		}
	}
	return v, nil
}

func TargetFramingSpec() Spec {
	return planningSpec{
		kind:     KindTargetFraming,
		category: CategoryPlans,
		validate: func(p json.RawMessage) error { _, err := decodeTargetFraming(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeTargetFraming(p)
			if err != nil {
				return Description{}, err
			}
			name := v.TargetName
			if name == "" {
				name = v.TargetGUID
			}
			o := ObjectRef{Entity: entityTarget, ID: v.TargetID, GUID: v.TargetGUID, Name: name}
			return Description{
				Title:   name + " · framing",
				Objects: []ObjectRef{o},
				Diffs:   []Diff{{Object: o, Field: "Framing", Before: quote(v.Before.Text()), After: quote(v.Framing.Text())}},
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeTargetFraming(p)
			if err != nil {
				return "", nil, err
			}
			v.Framing, v.Before = v.Before, v.Framing
			return KindTargetFraming, marshalRaw(v), nil
		},
	}
}
