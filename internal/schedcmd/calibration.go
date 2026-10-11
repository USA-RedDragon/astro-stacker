package schedcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"gorm.io/gorm"
)

const (
	KindCalibrationRequest Kind = "calibration.request"
	KindCalibrationCancel  Kind = "calibration.cancel"
)

type calibrationSpec struct {
	kind    Kind
	request bool
}

func CalibrationRequestSpec() Spec {
	return calibrationSpec{kind: KindCalibrationRequest, request: true}
}

func CalibrationCancelSpec() Spec { return calibrationSpec{kind: KindCalibrationCancel} }

func CalibrationSpecs() []Spec { return []Spec{CalibrationRequestSpec(), CalibrationCancelSpec()} }

func (s calibrationSpec) Kind() Kind             { return s.kind }
func (calibrationSpec) Category() Category       { return CategoryCalibration }
func (calibrationSpec) Destination() Destination { return DestinationApp }

func calibrationError(err error) error {
	var r calreq.RefusalError
	if errors.As(err, &r) {
		return fmt.Errorf("%w: %s", ErrInvalid, r.Msg)
	}
	return err
}

func (s calibrationSpec) decode(p json.RawMessage) (calreq.Payload, error) {
	v, err := decodeInto[calreq.Payload](p)
	if err != nil {
		return v, err
	}
	return v, calibrationError(v.Check(s.request))
}

func (s calibrationSpec) Validate(p json.RawMessage) error {
	_, err := s.decode(p)
	return err
}

func (s calibrationSpec) Describe(p json.RawMessage) (Description, error) {
	v, err := s.decode(p)
	if err != nil {
		return Description{}, err
	}
	o := ObjectRef{Entity: entityTarget, Name: v.Object}
	if v.TargetID != nil {
		o.ID = int64(*v.TargetID)
	}
	d := Description{Objects: []ObjectRef{o}}
	field := calibrationLabel(v.Type) + " request"
	if s.request {
		d.Title = fmt.Sprintf("Request %s for %s · night of %s", v.Type, v.Object, v.Night)
		d.Diffs = []Diff{{Object: o, Field: field, Before: quote("none"), After: quote("open")}}
		d.Note = "Saved in the app. The observatory takes the frames at its next chance, and the request is done once they are indexed."
	} else {
		d.Title = fmt.Sprintf("Cancel the %s request for %s · night of %s", v.Type, v.Object, v.Night)
		d.Diffs = []Diff{{Object: o, Field: field, Before: quote("open"), After: quote("cancelled")}}
		d.Note = "Saved in the app. The observatory stops taking frames for it at its next sync."
	}
	return d, nil
}

func calibrationLabel(typ string) string {
	switch typ {
	case "flats":
		return "Flats"
	case "darks":
		return "Darks"
	}
	return "Bias"
}

func (s calibrationSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	if _, err := s.decode(p); err != nil {
		return "", nil, err
	}
	if s.request {
		return KindCalibrationCancel, p, nil
	}
	return KindCalibrationRequest, p, nil
}

func (s calibrationSpec) ApplyApp(ctx context.Context, db *gorm.DB, p json.RawMessage) error {
	v, err := s.decode(p)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if s.request {
		_, err = calreq.Open(ctx, db, v, NewID(), AuthorFrom(ctx), now)
	} else {
		_, err = calreq.Cancel(ctx, db, v, AuthorFrom(ctx), now)
	}
	return calibrationError(err)
}
