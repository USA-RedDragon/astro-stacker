package schedauto

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
)

const (
	FieldMinimumTime     = "minimumtime"
	FieldMinimumAltitude = "minimumaltitude"
	stateClosed          = "Closed"
	entityProject        = "project"
)

// Values are the scheduling values of one project. Nil is a NULL column.
type Values struct {
	DitherEvery           *int     `json:"ditherEvery"`
	FilterSwitchFrequency *int     `json:"filterSwitchFrequency"`
	FlatsHandling         *int     `json:"flatsHandling"`
	SmartExposureOrder    *bool    `json:"smartExposureOrder"`
	MeridianWindow        *int     `json:"meridianWindow"`
	UseCustomHorizon      *bool    `json:"useCustomHorizon"`
	HorizonOffset         *float64 `json:"horizonOffset"`
	MaximumAltitude       *float64 `json:"maximumAltitude"`
	EnableGrader          *bool    `json:"enableGrader"`
	MinimumTime           *int     `json:"minimumTime"`
	MinimumAltitude       *float64 `json:"minimumAltitude"`
}

func ptr[T any](v T) *T { return &v }

// CurrentValues reads a project's values from the planning snapshot.
func CurrentValues(p planning.Project) Values {
	v := Values{DitherEvery: p.DitherEvery, FilterSwitchFrequency: p.FilterSwitchFrequency, FlatsHandling: p.FlatsHandling,
		SmartExposureOrder: p.SmartExposureOrder, MeridianWindow: p.MeridianWindow, UseCustomHorizon: p.UseCustomHorizon,
		HorizonOffset: p.HorizonOffset, MaximumAltitude: p.MaximumAltitude, EnableGrader: ptr(p.Grader), MinimumTime: ptr(p.MinimumTime)}
	if !p.MinAltitudeNull {
		v.MinimumAltitude = ptr(p.MinimumAltitude)
	}
	return v
}

type field struct {
	name string
	get  func(Values) any
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func fields() []field {
	return []field{
		{schedcmd.FieldDitherEvery, func(v Values) any { return deref(v.DitherEvery) }},
		{schedcmd.FieldFilterSwitchFrequency, func(v Values) any { return deref(v.FilterSwitchFrequency) }},
		{schedcmd.FieldFlatsHandling, func(v Values) any { return deref(v.FlatsHandling) }},
		{schedcmd.FieldSmartExposureOrder, func(v Values) any { return deref(v.SmartExposureOrder) }},
		{schedcmd.FieldMeridianWindow, func(v Values) any { return deref(v.MeridianWindow) }},
		{schedcmd.FieldUseCustomHorizon, func(v Values) any { return deref(v.UseCustomHorizon) }},
		{schedcmd.FieldHorizonOffset, func(v Values) any { return deref(v.HorizonOffset) }},
		{schedcmd.FieldMaximumAltitude, func(v Values) any { return deref(v.MaximumAltitude) }},
		{schedcmd.FieldEnableGrader, func(v Values) any { return deref(v.EnableGrader) }},
		{FieldMinimumTime, func(v Values) any { return deref(v.MinimumTime) }},
		{FieldMinimumAltitude, func(v Values) any { return deref(v.MinimumAltitude) }},
	}
}

func sameValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		return math.Abs(fa-fb) < 1e-6
	}
	return a == b
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// Changes are the fields where want differs from have; fields want leaves
// nil are kept.
func Changes(have, want Values) []schedcmd.FieldChange {
	var out []schedcmd.FieldChange
	for _, f := range fields() {
		w := f.get(want)
		if w == nil {
			continue
		}
		h := f.get(have)
		if sameValue(h, w) {
			continue
		}
		out = append(out, schedcmd.FieldChange{Field: f.name, Before: raw(h), After: raw(w)})
	}
	return out
}

// TargetResult is one target's window and automatic minimum time.
type TargetResult struct {
	ID       int      `json:"id"`
	GUID     string   `json:"guid"`
	Name     string   `json:"name"`
	Active   bool     `json:"active"`
	Body     string   `json:"body,omitempty"`
	Window   Window   `json:"window"`
	Source   string   `json:"windowSource"`
	MinTime  *MinTime `json:"minTime"`
	Position bool     `json:"hasPosition"`
}

// ProjectResult is what the stacker keeps one project at, and why.
type ProjectResult struct {
	ID            int                    `json:"id"`
	GUID          string                 `json:"guid"`
	Name          string                 `json:"name"`
	State         string                 `json:"state"`
	IsMosaic      bool                   `json:"isMosaic"`
	Current       Values                 `json:"current"`
	Desired       Values                 `json:"desired"`
	Changes       []schedcmd.FieldChange `json:"changes"`
	MinTime       *MinTime               `json:"minTime"`
	MinTimeTarget string                 `json:"minTimeTarget,omitempty"`
	MinAltitude   MinAltitude            `json:"minAltitude"`
	Targets       []TargetResult         `json:"targets"`
	Event         bool                   `json:"event"`
	Notes         []string               `json:"notes"`
	Left          string                 `json:"left,omitempty"`
}

// Horizon is the observatory's custom horizon when the NINA profile has
// one, which Target Scheduler then uses as a hard limit.
type Horizon struct {
	ProfileID string
	Custom    bool
	Horizon   *mosaics.Horizon
}

// BodyWindows gives a moving body's window above minAlt, false when the
// stacker has no nights for it.
type BodyWindows func(guid string, minAlt float64) (Window, bool)

// Inputs are what the plan is worked out from.
type Inputs struct {
	Global      schedglobal.Settings
	SwitchFloor int
	Nights      Nights
	Horizon     *Horizon
	BodyWindows BodyWindows
	Events      map[string]bool
}

// horizonFor is the horizon a project's limit follows and whether it
// should use the custom horizon; nil use when the report says nothing
// about the project's profile, so the project keeps its own setting.
func (in Inputs) horizonFor(p planning.Project) (*mosaics.Horizon, *bool) {
	if in.Horizon == nil {
		return nil, nil
	}
	if in.Horizon.ProfileID == "" || p.ProfileID == "" || !strings.EqualFold(in.Horizon.ProfileID, p.ProfileID) {
		return nil, nil
	}
	if !in.Horizon.Custom {
		return nil, ptr(false)
	}
	if in.Horizon.Horizon == nil || !in.Horizon.Horizon.Valid() {
		return nil, nil
	}
	return in.Horizon.Horizon, ptr(true)
}

// Plan works out the values the stacker keeps one project at.
func Plan(p planning.Project, in Inputs) ProjectResult {
	r := ProjectResult{ID: p.ID, GUID: p.GUID, Name: p.Name, State: p.State, IsMosaic: p.IsMosaic, Current: CurrentValues(p), Targets: []TargetResult{}}
	g := in.Global
	hz, useHorizon := in.horizonFor(p)
	r.MinAltitude = MinAltitudeFor(hz != nil)
	r.Desired = Values{DitherEvery: ptr(g.DitherEvery), FilterSwitchFrequency: ptr(g.FilterSwitchFrequency), FlatsHandling: ptr(g.FlatsHandling),
		SmartExposureOrder: ptr(g.SmartExposureOrder), MeridianWindow: ptr(g.MeridianWindow), UseCustomHorizon: useHorizon,
		HorizonOffset: ptr(0.0), MaximumAltitude: ptr(g.MaximumAltitude), EnableGrader: ptr(g.EnableGrader), MinimumAltitude: ptr(r.MinAltitude.Degrees)}
	limit := mosaics.Limit{Min: r.MinAltitude.Degrees, Horizon: hz}
	anyActive := false
	for _, t := range p.Targets {
		anyActive = anyActive || t.Active
	}
	for _, t := range p.Targets {
		tr := TargetResult{ID: t.ID, GUID: t.GUID, Name: t.Name, Active: t.Active, Body: t.BodyKind}
		switch {
		case t.BodyKind != "":
			tr.Source = "the body's nightly positions, above the minimum altitude without the horizon"
			if in.BodyWindows != nil {
				if w, ok := in.BodyWindows(t.GUID, max(r.MinAltitude.Degrees, HardMinAltitude)); ok {
					tr.Window, tr.Position = w, true
				}
			}
			if !tr.Position {
				tr.Source = "no nightly positions for this body yet"
			}
		case t.RAHours != nil && t.Dec != nil:
			tr.Position = true
			tr.Window = in.Nights.FixedWindow(*t.RAHours*15, *t.Dec, limit)
			tr.Source = "astronomical darkness above the minimum altitude"
			if hz != nil {
				tr.Source += " and the observatory horizon"
			}
		default:
			tr.Source = "no coordinates"
		}
		tr.MinTime = MinTimeFor(tr.Window, in.SwitchFloor)
		r.Targets = append(r.Targets, tr)
		if tr.MinTime == nil || (anyActive && !t.Active) {
			continue
		}
		if r.MinTime == nil || tr.MinTime.Minutes < r.MinTime.Minutes {
			r.MinTime, r.MinTimeTarget = tr.MinTime, t.Name
		}
	}
	if r.MinTime != nil {
		r.Desired.MinimumTime = ptr(r.MinTime.Minutes)
	}
	if in.Events[strings.ToLower(p.GUID)] {
		r.Event = true
		r.MinTime, r.MinTimeTarget = nil, ""
		r.Desired.MinimumTime, r.Desired.MinimumAltitude = nil, nil
		r.MinAltitude = MinAltitude{Degrees: p.MinimumAltitude, Basis: "a sky event: the event planner sets its minimum time and altitude for the event's own window"}
	}
	r.Notes = []string{}
	minAlt := p.MinimumAltitude
	if r.Desired.MinimumAltitude != nil {
		minAlt = *r.Desired.MinimumAltitude
	}
	if g.MaximumAltitude > 0 && g.MaximumAltitude <= minAlt {
		r.Desired.MaximumAltitude = nil
		r.Notes = append(r.Notes, fmt.Sprintf("the global maximum altitude %g° is not above this project's minimum altitude %g°, so its maximum altitude is left as it is", g.MaximumAltitude, minAlt))
	}
	if p.State == stateClosed {
		r.Left = "closed projects are left as they are"
		r.Changes = []schedcmd.FieldChange{}
		return r
	}
	r.Changes = Changes(r.Current, r.Desired)
	if r.Changes == nil {
		r.Changes = []schedcmd.FieldChange{}
	}
	return r
}

// Edit is the project.edit item that makes a project equal to its plan,
// nil when nothing differs.
func (r ProjectResult) Edit() *schedcmd.EditPayload {
	if len(r.Changes) == 0 {
		return nil
	}
	return &schedcmd.EditPayload{Entity: entityProject, ID: int64(r.ID), GUID: r.GUID, Name: r.Name, Changes: r.Changes}
}

// Summary is one line for a project, for the log and the UI.
func (r ProjectResult) Summary() string {
	mt := "minimum time unchanged"
	if r.MinTime != nil {
		mt = fmt.Sprintf("minimum time %d min (%s)", r.MinTime.Minutes, r.MinTime.Basis)
	}
	return fmt.Sprintf("%s: %s; minimum altitude %g° (%s)", r.Name, mt, r.MinAltitude.Degrees, r.MinAltitude.Basis)
}
