package planning

import (
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
)

// Least is the display progress of a target, project or catalogue object: its least complete enabled filter,
// measured against that filter's goal. Progress is nil when that filter has no goal measurement yet.
type Least struct {
	Enough      bool         `json:"enough"`
	Progress    *float64     `json:"progress"`
	Target      string       `json:"target,omitempty"`
	Filter      string       `json:"filter,omitempty"`
	NotMeasured string       `json:"notMeasured,omitempty"`
	Unmeasured  int          `json:"unmeasured"`
	Filters     int          `json:"filters"`
	Plateau     LeastPlateau `json:"plateau"`
}

type LeastPlateau struct {
	Progress       *float64 `json:"progress"`
	Hours          *float64 `json:"hours"`
	EffectiveHours float64  `json:"effectiveHours"`
	GainPerHourPct *float64 `json:"gainPerHourPct"`
	Target         string   `json:"target,omitempty"`
	Filter         string   `json:"filter,omitempty"`
	NotMeasured    string   `json:"notMeasured,omitempty"`
	Unmeasured     int      `json:"unmeasured"`
	Filters        int      `json:"filters"`
	GainPct        float64  `json:"gainPct"`
}

const (
	NoPlansReason   = "no enabled exposure plans"
	NoGoalReason    = "no goal set"
	CollectingFirst = "collecting subs for the first measurement"
	WaitingFirst    = "waiting for the first measurement"
	FailedReason    = "the last measurement failed"
	NoStackReason   = "no stack yet"
	NotMeasuredText = "not measured yet"
)

func goalProgressOf(g FilterGoal) *float64 {
	if g.Progress == nil || g.Progress.Unmeasured != "" {
		return nil
	}
	v := clamp01(g.Progress.Progress)
	if g.Progress.Done {
		v = 1
	} else {
		v = math.Min(v, 0.999)
	}
	return &v
}

func notMeasuredReason(g FilterGoal) string {
	switch {
	case g.Goal == nil:
		return NoGoalReason
	case g.Progress != nil && g.Progress.Unmeasured != "":
		return g.Progress.Unmeasured
	case g.Readiness != nil && g.Readiness.State == goals.StateUnmeasurable:
		return g.Readiness.Reason
	case g.Readiness != nil && g.Readiness.StackSubs < g.Readiness.MinSubs:
		return CollectingFirst
	case g.Status == StatusFailed && g.Error != "":
		return FailedReason + ": " + g.Error
	case g.Status == StatusFailed:
		return FailedReason
	case g.Readiness != nil:
		return WaitingFirst
	case g.Status == StatusNoMaster:
		return NoStackReason
	}
	return NotMeasuredText
}

func plateauReason(g FilterGoal) string {
	switch {
	case g.Measurement != nil:
		return g.Measurement.PlateauAt.Unmeasured
	case g.Readiness != nil && g.Readiness.StackSubs < g.Readiness.MinSubs:
		return CollectingFirst
	case g.Status == StatusFailed && g.Error != "":
		return FailedReason + ": " + g.Error
	case g.Status == StatusFailed:
		return FailedReason
	case g.Status == StatusNoMaster:
		return NoStackReason
	case g.Status == StatusNotMeasured:
		return WaitingFirst
	}
	return NotMeasuredText
}

func finishFilter(g *FilterGoal) {
	if g.Readiness != nil && g.Readiness.State == goals.StateCollecting {
		g.Enough = false
	}
	g.GoalProgress = goalProgressOf(*g)
	if g.GoalProgress == nil {
		g.NotMeasured = notMeasuredReason(*g)
	}
	g.PlateauAt = goals.Plateau{GainPct: goals.PlateauGainPct}
	if g.Measurement != nil {
		g.PlateauAt = g.Measurement.PlateauAt
	}
	if g.PlateauAt.Progress == nil {
		g.PlateauAt.Unmeasured = plateauReason(*g)
	}
}

func leastPlateauOf(target string, gs []FilterGoal) LeastPlateau {
	lp := LeastPlateau{Target: target, Filters: len(gs), GainPct: goals.PlateauGainPct}
	var w *FilterGoal
	for i := range gs {
		g := &gs[i]
		if g.PlateauAt.Progress == nil {
			lp.Unmeasured++
		}
		if w == nil || filterLess(false, g.PlateauAt.Progress, false, w.PlateauAt.Progress) {
			w = g
		}
	}
	if w == nil {
		lp.NotMeasured = NoPlansReason
		return lp
	}
	lp.Filter, lp.Progress, lp.Hours = w.Filter, w.PlateauAt.Progress, w.PlateauAt.Hours
	if w.Measurement != nil {
		lp.EffectiveHours, lp.GainPerHourPct = w.Measurement.EffectiveHours, w.Measurement.GainPerHourPct
	}
	if lp.Progress == nil {
		lp.NotMeasured = w.PlateauAt.Unmeasured
	}
	return lp
}

func PlateauLess(a, b LeastPlateau) bool {
	if a.Filters == 0 || b.Filters == 0 {
		return a.Filters == 0 && b.Filters != 0
	}
	return filterLess(false, a.Progress, false, b.Progress)
}

func filterLess(enoughA bool, pa *float64, enoughB bool, pb *float64) bool {
	rank := func(enough bool, p *float64) (bool, float64) {
		if enough {
			return true, 1
		}
		if p == nil {
			return false, 0
		}
		return true, *p
	}
	ma, va := rank(enoughA, pa)
	mb, vb := rank(enoughB, pb)
	if ma != mb {
		return !ma
	}
	return va < vb
}

func leastOfGoals(target string, gs []FilterGoal) Least {
	l := Least{Target: target, Filters: len(gs), Enough: len(gs) > 0, Plateau: leastPlateauOf(target, gs)}
	if len(gs) == 0 {
		l.NotMeasured = NoPlansReason
		return l
	}
	var w *FilterGoal
	for i := range gs {
		g := &gs[i]
		l.Enough = l.Enough && g.Enough
		if !g.Enough && g.GoalProgress == nil {
			l.Unmeasured++
		}
		if w == nil || filterLess(g.Enough, g.GoalProgress, w.Enough, w.GoalProgress) {
			w = g
		}
	}
	l.Filter = w.Filter
	switch {
	case l.Enough:
		one := 1.0
		l.Progress = &one
	case w.GoalProgress != nil:
		v := *w.GoalProgress
		l.Progress = &v
	default:
		l.NotMeasured = w.NotMeasured
	}
	return l
}

// LeastLess orders two Least values so the least complete comes first: a missing measurement ranks below any
// measured progress, and enough data ranks above everything.
func LeastLess(a, b Least) bool {
	if a.Filters == 0 || b.Filters == 0 {
		return a.Filters == 0 && b.Filters != 0
	}
	return filterLess(a.Enough, a.Progress, b.Enough, b.Progress)
}

func leastOfTargets(ts []Target) Least {
	var w *Least
	var wp *LeastPlateau
	enough, unmeasured, filters, plateauUnmeasured := false, 0, 0, 0
	for i := range ts {
		if !ts[i].Active || ts[i].Least.Filters == 0 {
			continue
		}
		l := ts[i].Least
		if w == nil {
			enough = l.Enough
		}
		enough = enough && l.Enough
		unmeasured += l.Unmeasured
		filters += l.Filters
		plateauUnmeasured += l.Plateau.Unmeasured
		if w == nil || LeastLess(l, *w) {
			w = &l
		}
		if wp == nil || PlateauLess(l.Plateau, *wp) {
			p := l.Plateau
			wp = &p
		}
	}
	if w == nil {
		return Least{NotMeasured: NoPlansReason, Plateau: LeastPlateau{NotMeasured: NoPlansReason, GainPct: goals.PlateauGainPct}}
	}
	out := *w
	out.Enough, out.Unmeasured, out.Filters = enough, unmeasured, filters
	out.Plateau = *wp
	out.Plateau.Unmeasured, out.Plateau.Filters = plateauUnmeasured, filters
	return out
}

// Text is a one-line summary of the least complete filter, e.g. "needs more data: Luminance at 4% of its goal".
func (l Least) Text() string {
	switch {
	case l.Filters == 0:
		return NoPlansReason
	case l.Enough:
		return "enough data in every filter"
	case l.Progress == nil:
		return fmt.Sprintf("needs more data: %s %s (%s)", l.Filter, NotMeasuredText, l.NotMeasured)
	}
	return fmt.Sprintf("needs more data: %s least complete at %.0f%% of its goal", l.Filter, math.Floor(*l.Progress*100))
}
