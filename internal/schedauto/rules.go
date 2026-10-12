package schedauto

import (
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
)

const (
	// MaxSwitchShare is the most of the night target switches may take.
	MaxSwitchShare = 0.15
	// HardMinAltitude is the minimum altitude until the observatory
	// horizon is set: below it there are too few subs to judge.
	HardMinAltitude = schedcmd.HardMinAltitude
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

// MinTimeFor is always the switching floor, so a stint never spends more
// than MaxSwitchShare switching. A target whose window is shorter is not
// picked that night, and is picked once its window grows. The window is
// kept for the reason.
func MinTimeFor(w Window, floor int) *MinTime {
	m := &MinTime{Minutes: floor, Basis: fmt.Sprintf("the %d min switching floor", floor)}
	var best *float64
	label := ""
	if w.Tonight != nil && *w.Tonight > 0 {
		best, label = w.Tonight, "tonight"
	}
	if w.Typical != nil && *w.Typical > 0 && (best == nil || *w.Typical < *best) {
		best, label = w.Typical, fmt.Sprintf("typical over the next %d nights", TypicalNights)
	}
	if best == nil {
		return m
	}
	win := fmt.Sprintf("%s window %s", windowText(*best), label)
	m.Window, m.Hours = &win, *best
	if *best*60 < float64(floor) {
		m.Basis = fmt.Sprintf("the %d min switching floor; its %s is shorter, so it is not picked until the window grows", floor, win)
	}
	return m
}

// MinAltitude is a project's automatic minimum altitude and why.
type MinAltitude struct {
	Degrees float64 `json:"degrees"`
	Horizon bool    `json:"horizon"`
	Basis   string  `json:"basis"`
}

// MinAltitudeFor is the hard limit: HardMinAltitude until the project
// follows the observatory horizon, then the horizon alone. Above it the
// plugin weighs each exposure by its kept rate at the target's altitude.
func MinAltitudeFor(horizon bool) MinAltitude {
	if horizon {
		return MinAltitude{Degrees: 0, Horizon: true, Basis: "the observatory horizon is the hard limit"}
	}
	return MinAltitude{Degrees: HardMinAltitude, Basis: fmt.Sprintf("%g°: below it there are too few subs to judge, and there is no observatory horizon yet", HardMinAltitude)}
}
