package schedauto

import (
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
)

const (
	// MaxSwitchShare is the most of the night target switches may take.
	MaxSwitchShare = 0.15
	// MinTimeMax is the longest automatic minimum time, in minutes.
	MinTimeMax = 90
	// MinTimeStep is what minimum times are rounded to, in minutes.
	MinTimeStep = 5
	// DefaultSwitchMinutes is the switch time used until the stacker has
	// measured one: the median of the NINA logs of 2026-10-07 to 2026-10-12.
	DefaultSwitchMinutes = 3.9
)

// SwitchFloor is the shortest minimum time that keeps switching at or under
// MaxSwitchShare when every stint lasts at least that long and a switch
// takes switchMinutes: stint/(stint+switch) >= 1-MaxSwitchShare.
func SwitchFloor(switchMinutes float64) int {
	if !(switchMinutes > 0) {
		switchMinutes = DefaultSwitchMinutes
	}
	need := switchMinutes * (1 - MaxSwitchShare) / MaxSwitchShare
	return int(math.Ceil(need/MinTimeStep-1e-9)) * MinTimeStep
}

// MinTime is the automatic minimum time of one target and why.
type MinTime struct {
	Minutes int     `json:"minutes"`
	Window  *string `json:"window,omitempty"`
	Hours   float64 `json:"windowHours,omitempty"`
	Basis   string  `json:"basis"`
}

func windowText(h float64) string {
	if h < 2 {
		return fmt.Sprintf("%d min", int(math.Round(h*60)))
	}
	return fmt.Sprintf("%.1f h", h)
}

// MinTimeFor is half of the shorter of tonight's and the typical window,
// rounded down to MinTimeStep, from floor to MinTimeMax. A target whose
// window is shorter than floor gets its whole window rounded down, at least
// MinTimeStep, so it can still be imaged. With no window it is nil.
func MinTimeFor(w Window, floor int) *MinTime {
	type pick struct {
		label string
		h     float64
	}
	var picks []pick
	if w.Tonight != nil && *w.Tonight > 0 {
		picks = append(picks, pick{"tonight", *w.Tonight})
	}
	if w.Typical != nil && *w.Typical > 0 {
		picks = append(picks, pick{fmt.Sprintf("typical over the next %d nights", TypicalNights), *w.Typical})
	}
	if len(picks) == 0 {
		return nil
	}
	p := picks[0]
	for _, q := range picks[1:] {
		if q.h < p.h {
			p = q
		}
	}
	win := fmt.Sprintf("%s window %s", windowText(p.h), p.label)
	m := MinTime{Window: &win, Hours: p.h}
	whole := int(math.Floor(p.h*60/MinTimeStep)) * MinTimeStep
	half := int(math.Floor(p.h*60/2/MinTimeStep)) * MinTimeStep
	switch {
	case whole < floor:
		m.Minutes = max(MinTimeStep, whole)
		m.Basis = fmt.Sprintf("the whole %s, which is shorter than the %d min switching floor", win, floor)
	case half < floor:
		m.Minutes = floor
		m.Basis = fmt.Sprintf("the %d min switching floor; half the %s is shorter", floor, win)
	case half > MinTimeMax:
		m.Minutes = MinTimeMax
		m.Basis = fmt.Sprintf("the %d min cap; half the %s is longer", MinTimeMax, win)
	default:
		m.Minutes = half
		m.Basis = fmt.Sprintf("half the %s", win)
	}
	return &m
}

// MinAltitude is a project's automatic minimum altitude and why.
type MinAltitude struct {
	Degrees float64 `json:"degrees"`
	Class   string  `json:"class"`
	Basis   string  `json:"basis"`
	Reason  string  `json:"reason"`
	Horizon bool    `json:"horizon"`
}

func className(nb bool) string {
	if nb {
		return "narrowband"
	}
	return "broadband"
}

// MinAltitudeFor is the lowest altitude floor among the filter classes a
// project images, so no filter is kept from altitudes where it still keeps
// enough. classes empty means broadband and narrowband.
func MinAltitudeFor(floors []moonkeep.AltitudeFloor, classes []bool, horizon bool) MinAltitude {
	if len(classes) == 0 {
		classes = []bool{false, true}
	}
	var best *moonkeep.AltitudeFloor
	for _, nb := range classes {
		f := moonkeep.AltitudeFloorFor(floors, nb)
		if best == nil || f.Floor < best.Floor {
			best = &f
		}
	}
	return MinAltitude{Degrees: best.Floor, Class: className(best.Narrowband), Basis: best.Basis, Reason: best.Reason, Horizon: horizon}
}
