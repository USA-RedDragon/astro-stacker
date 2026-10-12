package schedcmd

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"
)

const (
	KindProjectEdit      Kind = "project.edit"
	KindTargetEdit       Kind = "target.edit"
	KindExposurePlanEdit Kind = "exposureplan.edit"
	KindSkip             Kind = "scheduler.skip"
	KindUnskip           Kind = "scheduler.unskip"
	KindPause            Kind = "scheduler.pause"
	KindResume           Kind = "scheduler.resume"
	KindReplan           Kind = "scheduler.replan"
	KindAbortExposure    Kind = "exposure.abort"
)

const (
	entityProject = "project"
	entityTarget  = "target"
	labelProject  = "Project"
	fieldState    = "State"
	fieldSkipped  = "Skipped"
	mountTrack    = "track"
	mountPark     = "park"
	scopeTarget   = "target"
	scopeProject  = "project"
)

func ProjectStates() map[int]string {
	return map[int]string{0: "Draft", 1: "Active", 2: "Inactive", 3: "Closed"}
}

func ProjectPriorities() map[int]string {
	return map[int]string{0: "Low", 1: "Normal", 2: "High"}
}

func ProjectEdit() EditSpec {
	fields := map[string]FieldSpec{
		"state":           {Label: fieldState, Format: EnumFormat(ProjectStates())},
		"priority":        {Label: "Priority", Format: EnumFormat(ProjectPriorities())},
		"minimumtime":     {Label: "Minimum time", Format: SuffixFormat(" min")},
		"minimumaltitude": {Label: "Minimum altitude", Format: SuffixFormat("°")},
	}
	maps.Copy(fields, projectSettingFields())
	return EditSpec{
		KindName:     KindProjectEdit,
		Entity:       entityProject,
		EntityLabel:  labelProject,
		CategoryName: CategoryPriority,
		Fields:       fields,
	}
}

func TargetEdit() EditSpec {
	return EditSpec{
		KindName:     KindTargetEdit,
		Entity:       entityTarget,
		EntityLabel:  "Target",
		CategoryName: CategoryPriority,
		Fields: map[string]FieldSpec{
			"active": {Label: "Active", Format: BoolFormat("Active", "Inactive")},
		},
	}
}

func ExposurePlanEdit() EditSpec {
	return EditSpec{
		KindName:     KindExposurePlanEdit,
		Entity:       "exposureplan",
		EntityLabel:  "Exposure plan",
		CategoryName: CategoryPlans,
		Fields: map[string]FieldSpec{
			"enabled": {Label: "Enabled", Format: BoolFormat("On", "Off")},
			"desired": {Label: "Desired"},
		},
	}
}

type SkipPayload struct {
	Scope       string `json:"scope"`
	TargetID    int64  `json:"target_id"`
	TargetName  string `json:"target_name"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name"`
	Minutes     int    `json:"minutes"`
}

type PausePayload struct {
	Mount              string `json:"mount"`
	ResumeAfterMinutes int    `json:"resume_after_minutes,omitempty"`
	ResumeAt           string `json:"resume_at,omitempty"`
}

type controlSpec struct {
	kind     Kind
	validate func(json.RawMessage) error
	describe func(json.RawMessage) (Description, error)
	inverse  func(json.RawMessage) (Kind, json.RawMessage, error)
}

func (s controlSpec) Kind() Kind                                      { return s.kind }
func (s controlSpec) Category() Category                              { return CategoryControl }
func (s controlSpec) Destination() Destination                        { return DestinationObservatory }
func (s controlSpec) Validate(p json.RawMessage) error                { return s.validate(p) }
func (s controlSpec) Describe(p json.RawMessage) (Description, error) { return s.describe(p) }
func (s controlSpec) Inverse(p json.RawMessage) (Kind, json.RawMessage, error) {
	if s.inverse == nil {
		return "", nil, ErrNoInverse
	}
	return s.inverse(p)
}

func decodeSkip(p json.RawMessage) (SkipPayload, error) {
	var s SkipPayload
	if err := json.Unmarshal(p, &s); err != nil {
		return s, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	switch s.Scope {
	case scopeTarget:
		if s.TargetID <= 0 {
			return s, fmt.Errorf("%w: missing target_id", ErrInvalid)
		}
	case scopeProject:
		if s.ProjectID <= 0 {
			return s, fmt.Errorf("%w: missing project_id", ErrInvalid)
		}
	default:
		return s, fmt.Errorf("%w: scope must be target or project", ErrInvalid)
	}
	if s.Minutes < 0 {
		return s, fmt.Errorf("%w: negative minutes", ErrInvalid)
	}
	return s, nil
}

func skipObject(s SkipPayload) ObjectRef {
	if s.Scope == scopeProject {
		return ObjectRef{Entity: entityProject, ID: s.ProjectID, Name: s.ProjectName}
	}
	return ObjectRef{Entity: entityTarget, ID: s.TargetID, Name: s.TargetName}
}

func skipFor(s SkipPayload) string {
	if s.Minutes == 0 {
		return "rest of tonight"
	}
	return fmt.Sprintf("%d min", s.Minutes)
}

func quote(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}

func SkipSpec() Spec {
	return controlSpec{
		kind:     KindSkip,
		validate: func(p json.RawMessage) error { _, err := decodeSkip(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			s, err := decodeSkip(p)
			if err != nil {
				return Description{}, err
			}
			o := skipObject(s)
			return Description{
				Title:   "Skip " + o.Name + " · " + skipFor(s),
				Objects: []ObjectRef{o},
				Diffs:   []Diff{{Object: o, Field: fieldSkipped, After: quote(skipFor(s))}},
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			if _, err := decodeSkip(p); err != nil {
				return "", nil, err
			}
			return KindUnskip, p, nil
		},
	}
}

func UnskipSpec() Spec {
	return controlSpec{
		kind:     KindUnskip,
		validate: func(p json.RawMessage) error { _, err := decodeSkip(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			s, err := decodeSkip(p)
			if err != nil {
				return Description{}, err
			}
			o := skipObject(s)
			return Description{
				Title:   "Stop skipping " + o.Name,
				Objects: []ObjectRef{o},
				Diffs:   []Diff{{Object: o, Field: fieldSkipped, After: quote("No")}},
			}, nil
		},
		inverse: func(p json.RawMessage) (Kind, json.RawMessage, error) {
			if _, err := decodeSkip(p); err != nil {
				return "", nil, err
			}
			return KindSkip, p, nil
		},
	}
}

func decodePause(p json.RawMessage) (PausePayload, error) {
	var s PausePayload
	if len(p) == 0 || string(p) == "null" {
		return PausePayload{Mount: mountTrack}, nil
	}
	if err := json.Unmarshal(p, &s); err != nil {
		return s, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if s.Mount == "" {
		s.Mount = mountTrack
	}
	if s.Mount != mountTrack && s.Mount != mountPark {
		return s, fmt.Errorf("%w: mount must be track or park", ErrInvalid)
	}
	if s.ResumeAfterMinutes < 0 {
		return s, fmt.Errorf("%w: negative resume_after_minutes", ErrInvalid)
	}
	return s, nil
}

func pauseLabel(s PausePayload) string {
	l := "Paused, mount tracking"
	if s.Mount == mountPark {
		l = "Paused, mount parked"
	}
	switch {
	case s.ResumeAfterMinutes > 0:
		l += fmt.Sprintf(", resumes after %d min", s.ResumeAfterMinutes)
	case s.ResumeAt != "":
		l += ", resumes at " + s.ResumeAt
	}
	return l
}

func schedulerObject() ObjectRef { return ObjectRef{Entity: "scheduler", Name: "Scheduler"} }

func PauseSpec() Spec {
	return controlSpec{
		kind:     KindPause,
		validate: func(p json.RawMessage) error { _, err := decodePause(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			s, err := decodePause(p)
			if err != nil {
				return Description{}, err
			}
			return Description{
				Title:   "Pause the scheduler",
				Objects: []ObjectRef{schedulerObject()},
				Diffs:   []Diff{{Object: schedulerObject(), Field: fieldState, After: quote(pauseLabel(s))}},
			}, nil
		},
		inverse: func(json.RawMessage) (Kind, json.RawMessage, error) {
			return KindResume, json.RawMessage(`{}`), nil
		},
	}
}

func ResumeSpec() Spec {
	return controlSpec{
		kind:     KindResume,
		validate: func(json.RawMessage) error { return nil },
		describe: func(json.RawMessage) (Description, error) {
			return Description{
				Title:   "Resume the scheduler",
				Objects: []ObjectRef{schedulerObject()},
				Diffs:   []Diff{{Object: schedulerObject(), Field: fieldState, After: quote("Running")}},
			}, nil
		},
		inverse: func(json.RawMessage) (Kind, json.RawMessage, error) {
			return KindPause, json.RawMessage(`{"mount":"track"}`), nil
		},
	}
}

func ReplanSpec() Spec {
	return controlSpec{
		kind:     KindReplan,
		validate: func(json.RawMessage) error { return nil },
		describe: func(json.RawMessage) (Description, error) {
			return Description{Title: "Re-plan now", Objects: []ObjectRef{schedulerObject()}}, nil
		},
	}
}

type AbortExposurePayload struct {
	PlanID     int64     `json:"plan_id"`
	StartedAt  time.Time `json:"started_at"`
	TargetName string    `json:"target_name,omitempty"`
	Filter     string    `json:"filter,omitempty"`
}

func decodeAbortExposure(p json.RawMessage) (AbortExposurePayload, error) {
	var s AbortExposurePayload
	if len(p) == 0 || string(p) == "null" {
		return s, fmt.Errorf("%w: missing plan_id", ErrInvalid)
	}
	if err := json.Unmarshal(p, &s); err != nil {
		return s, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if s.PlanID <= 0 {
		return s, fmt.Errorf("%w: missing plan_id", ErrInvalid)
	}
	if s.StartedAt.IsZero() {
		return s, fmt.Errorf("%w: missing started_at", ErrInvalid)
	}
	return s, nil
}

func abortedExposure(s AbortExposurePayload) string {
	what := "Exposure"
	if s.Filter != "" {
		what = s.Filter + " exposure"
	}
	if s.TargetName != "" {
		what += " on " + s.TargetName
	}
	return what + ", started " + s.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC")
}

type abortExposureSpec struct {
	controlSpec
}

func (abortExposureSpec) UnsentMessage() string {
	return "The observatory PC did not answer, so nothing was aborted. An abort is not queued or retried, because it only applies to the exposure running when you sent it."
}

func AbortExposureSpec() Spec {
	return abortExposureSpec{controlSpec{
		kind:     KindAbortExposure,
		validate: func(p json.RawMessage) error { _, err := decodeAbortExposure(p); return err },
		describe: func(p json.RawMessage) (Description, error) {
			s, err := decodeAbortExposure(p)
			if err != nil {
				return Description{}, err
			}
			title := "Abort the exposure"
			if s.TargetName != "" {
				title += " on " + s.TargetName
			}
			return Description{
				Title:   title,
				Objects: []ObjectRef{schedulerObject()},
				Diffs:   []Diff{{Object: schedulerObject(), Field: "Exposure", Before: quote(abortedExposure(s)), After: quote("Aborted, partial frame discarded")}},
				Note:    "Applies at once. The partial frame is not saved and cannot be recovered.",
			}, nil
		},
	}}
}

func Builtin() []Spec {
	return append([]Spec{
		ProjectEdit(),
		TargetEdit(),
		ExposurePlanEdit(),
		SkipSpec(),
		UnskipSpec(),
		PauseSpec(),
		ResumeSpec(),
		ReplanSpec(),
		AbortExposureSpec(),
		MosaicAdoptSpec(),
		GlobalWeightsSpec(),
		GlobalSchedulingSpec(),
	}, append(append(PlanningSpecs(), MatchingSpecs()...), CalibrationSpecs()...)...)
}
