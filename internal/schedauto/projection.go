package schedauto

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	SettingsCurrent   = "current"
	SettingsAutomatic = "automatic"
	sunsetAltitude    = -0.833
)

// Previewer runs the plugin's night simulation.
type Previewer interface {
	PreviewWith(ctx context.Context, req observatory.PreviewRequest) (json.RawMessage, error)
}

// Projection is a simulated night and the share of it target switches
// would take at the measured switch time.
type Projection struct {
	Settings      string     `json:"settings"`
	Overrides     int        `json:"overrides"`
	Start         *time.Time `json:"start,omitempty"`
	End           *time.Time `json:"end,omitempty"`
	Switches      int        `json:"switches"`
	Targets       int        `json:"targets"`
	ImagingHours  float64    `json:"imagingHours"`
	SwitchMinutes float64    `json:"switchMinutes"`
	Share         float64    `json:"share"`
}

// ProjectionView compares tonight with the projects as they are and with
// the automatic values.
type ProjectionView struct {
	ComputedAt       time.Time  `json:"computedAt"`
	PerSwitchMinutes float64    `json:"perSwitchMinutes"`
	MaxSwitchShare   float64    `json:"maxSwitchShare"`
	Current          Projection `json:"current"`
	Automatic        Projection `json:"automatic"`
	Basis            string     `json:"basis"`
}

type previewBlock struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Wait     bool      `json:"wait"`
	TargetID int64     `json:"target_id"`
}

// ParseProjection counts the switches of a simulated night: every block
// that images a different target from the one imaged before it, waits
// between them included. The share is the switch time over the imaging
// time plus the switch time; waits count for neither.
func ParseProjection(raw json.RawMessage, perSwitch float64) (Projection, error) {
	var v struct {
		Blocks []previewBlock `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Projection{}, fmt.Errorf("read the simulated night: %w", err)
	}
	var p Projection
	var last int64
	var imaging time.Duration
	seen := map[int64]bool{}
	for _, b := range v.Blocks {
		if b.Wait || b.TargetID == 0 {
			continue
		}
		if p.Start == nil {
			s := b.Start
			p.Start = &s
		}
		e := b.End
		p.End = &e
		imaging += b.End.Sub(b.Start)
		if last != 0 && b.TargetID != last {
			p.Switches++
		}
		last = b.TargetID
		seen[b.TargetID] = true
	}
	p.Targets = len(seen)
	p.ImagingHours = round2(imaging.Hours())
	p.SwitchMinutes = round2(float64(p.Switches) * perSwitch)
	if total := imaging.Minutes() + p.SwitchMinutes; total > 0 {
		p.Share = math.Round(p.SwitchMinutes/total*1000) / 1000
	}
	return p, nil
}

func number(v float64) json.RawMessage {
	return json.RawMessage(strconv.FormatFloat(v, 'f', -1, 64))
}

// Overrides are the preview overrides that give every project its
// automatic minimum time and minimum altitude.
func Overrides(projects []ProjectResult) []observatory.Override {
	out := []observatory.Override{}
	for _, p := range projects {
		if p.Left != "" {
			continue
		}
		if p.Desired.MinimumTime != nil && (p.Current.MinimumTime == nil || *p.Current.MinimumTime != *p.Desired.MinimumTime) {
			out = append(out, observatory.Override{Entity: entityProject, ID: int64(p.ID), Field: FieldMinimumTime, Value: number(float64(*p.Desired.MinimumTime))})
		}
		if p.Desired.MinimumAltitude != nil && (p.Current.MinimumAltitude == nil || !sameValue(*p.Current.MinimumAltitude, *p.Desired.MinimumAltitude)) {
			out = append(out, observatory.Override{Entity: entityProject, ID: int64(p.ID), Field: FieldMinimumAltitude, Value: number(*p.Desired.MinimumAltitude)})
		}
	}
	return out
}

// Sunset is when the Sun sets on the night sky.Site.Tonight gives for now,
// or now when that is later.
func Sunset(site sky.Site, now time.Time) time.Time {
	n := site.Tonight(now, windowStep)
	for t := n.Start; t.Before(n.Start.Add(24 * time.Hour)); t = t.Add(windowStep) {
		if site.SunAltitude(t) < sunsetAltitude {
			if t.Before(now) {
				return now
			}
			return t
		}
	}
	return now
}

// Projection simulates tonight twice through the plugin, as the projects
// are and with the automatic minimum times and altitudes, and works out the
// switching share of each at the measured switch time. Nothing is changed.
func (s *Service) Projection(ctx context.Context, pv Previewer) (*ProjectionView, error) {
	if pv == nil {
		return nil, fmt.Errorf("no observatory scheduler to simulate the night on")
	}
	s.mu.Lock()
	cached := s.proj
	s.mu.Unlock()
	if cached != nil && s.now().Sub(cached.ComputedAt) < cacheFor {
		return cached, nil
	}
	res, err := s.Current(ctx)
	if err != nil {
		return nil, err
	}
	site, err := s.Site(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoSite, err)
	}
	per := res.Overhead.MeanMinutes
	if !(per > 0) {
		per = DefaultSwitchMinutes
	}
	start := Sunset(site, s.now())
	auto := Overrides(res.Projects)
	v := &ProjectionView{ComputedAt: s.now().UTC(), PerSwitchMinutes: per, MaxSwitchShare: MaxSwitchShare,
		Basis: "The plugin's simulation of tonight from sunset, which has no slew, centering or autofocus time, with each switch costing the measured mean switch time. " +
			"A switch is a block on a different target from the one before it. The automatic run changes only the minimum times and altitudes; the simulation can't change the other settings."}
	for _, run := range []struct {
		p         *Projection
		settings  string
		overrides []observatory.Override
	}{{&v.Current, SettingsCurrent, []observatory.Override{}}, {&v.Automatic, SettingsAutomatic, auto}} {
		raw, err := pv.PreviewWith(ctx, observatory.PreviewRequest{Start: &start, Overrides: run.overrides})
		if err != nil {
			return nil, fmt.Errorf("simulate tonight with the %s values: %w", run.settings, err)
		}
		p, err := ParseProjection(raw, per)
		if err != nil {
			return nil, err
		}
		p.Settings, p.Overrides = run.settings, len(run.overrides)
		*run.p = p
	}
	s.mu.Lock()
	s.proj = v
	s.mu.Unlock()
	return v, nil
}
