package schedcmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	EventPriorityNormal = "normal"
	EventPriorityHigh   = "high"
	BodyKindComet       = "comet"
	BodyKindAsteroid    = "asteroid"
	BodyKindEvent       = "event"
	// MaxEventWindow is the longest event window the plugin accepts.
	MaxEventWindow = 400 * 24 * time.Hour
	MaxDesignation = 200
)

// EventWindow limits a target to a span of time. A normal-priority event
// target only fills gaps; a high-priority one may end another target's plan
// at the next exposure boundary while it is inside its window.
type EventWindow struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Priority   string    `json:"priority"`
	EventID    string    `json:"event_id,omitempty"`
	IgnoreMoon bool      `json:"ignore_moon,omitempty"`
}

func (w EventWindow) Validate() error {
	if w.Start.IsZero() || w.End.IsZero() || !w.End.After(w.Start) {
		return fmt.Errorf("%w: the event window needs a start before its end", ErrInvalid)
	}
	if w.End.Sub(w.Start) > MaxEventWindow {
		return fmt.Errorf("%w: the event window is longer than %d days", ErrInvalid, int(MaxEventWindow.Hours()/24))
	}
	if w.Priority != EventPriorityNormal && w.Priority != EventPriorityHigh {
		return fmt.Errorf("%w: event priority %q, want normal or high", ErrInvalid, w.Priority)
	}
	return nil
}

func (w EventWindow) Text() string {
	s := "event window " + w.Start.UTC().Format("2006-01-02 15:04") + " to " + w.End.UTC().Format("2006-01-02 15:04") + " UTC, "
	if w.Priority == EventPriorityHigh {
		s += "high priority (may interrupt at the next exposure boundary)"
	} else {
		s += "fills gaps"
	}
	if w.IgnoreMoon {
		s += ", no moon avoidance"
	}
	return s
}

func (t NewTarget) checkBody() error {
	d := strings.TrimSpace(t.Designation)
	if len(d) > MaxDesignation {
		return fmt.Errorf("%w: %s designation is longer than %d characters", ErrInvalid, t.Name, MaxDesignation)
	}
	switch t.BodyKind {
	case "", BodyKindComet, BodyKindAsteroid, BodyKindEvent:
	default:
		return fmt.Errorf("%w: %s body kind %q", ErrInvalid, t.Name, t.BodyKind)
	}
	if t.BodyKind != "" && d == "" {
		return fmt.Errorf("%w: %s has a body kind but no designation", ErrInvalid, t.Name)
	}
	if err := checkFraming(t.RotationDeg, t.OffsetFrac); err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}
	if t.RotationDeg != nil && d == "" {
		return fmt.Errorf("%w: %s has framing but no designation", ErrInvalid, t.Name)
	}
	if t.Window != nil {
		if err := t.Window.Validate(); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
	}
	return nil
}

type TargetWindowPayload struct {
	TargetID   int64        `json:"target_id"`
	TargetGUID string       `json:"target_guid"`
	TargetName string       `json:"target_name"`
	Window     *EventWindow `json:"window"`
	Before     *EventWindow `json:"before,omitempty"`
}

func decodeTargetWindow(p json.RawMessage) (TargetWindowPayload, error) {
	v, err := decodeInto[TargetWindowPayload](p)
	if err != nil {
		return v, err
	}
	if v.TargetGUID == "" {
		return v, fmt.Errorf("%w: missing target_guid", ErrInvalid)
	}
	for _, w := range []*EventWindow{v.Window, v.Before} {
		if w != nil {
			if err := w.Validate(); err != nil {
				return v, err
			}
		}
	}
	return v, nil
}

func windowText(w *EventWindow) string {
	if w == nil {
		return "none"
	}
	return w.Text()
}

func TargetWindowSpec() Spec {
	return planningSpec{
		kind:     KindTargetWindow,
		category: CategoryPriority,
		validate: func(p json.RawMessage) error { _, err := decodeTargetWindow(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			v, err := decodeTargetWindow(p)
			if err != nil {
				return Description{}, err
			}
			name := v.TargetName
			if name == "" {
				name = v.TargetGUID
			}
			o := ObjectRef{Entity: entityTarget, ID: v.TargetID, GUID: v.TargetGUID, Name: name}
			return Description{
				Title:   name + " · event window",
				Objects: []ObjectRef{o},
				Diffs:   []Diff{{Object: o, Field: "Event window", Before: quote(windowText(v.Before)), After: quote(windowText(v.Window))}},
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			v, err := decodeTargetWindow(p)
			if err != nil {
				return "", nil, err
			}
			v.Window, v.Before = v.Before, v.Window
			return KindTargetWindow, marshalRaw(v), nil
		},
	}
}
