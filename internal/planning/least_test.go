package planning

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
)

func measuredGoal(f string, progress float64, done bool) FilterGoal {
	g := FilterGoal{Filter: f, Goal: &goals.Goal{}, Status: StatusMeasured, Progress: &goals.Progress{Progress: progress, Done: done}, Enough: done}
	finishFilter(&g)
	return g
}

func TestLeastIsTheWeakestFilterNotTheAverage(t *testing.T) {
	t.Parallel()
	gs := []FilterGoal{measuredGoal("Luminance", 0.04, false), measuredGoal("Red", 0.48, false), measuredGoal(filterHa, 0.91, false)}
	l := leastOfGoals("M 31", gs)
	if l.Enough || l.Filter != "Luminance" || l.Progress == nil || math.Abs(*l.Progress-0.04) > 1e-9 || l.Unmeasured != 0 || l.Filters != 3 {
		t.Fatalf("%+v", l)
	}
	if l.Text() != "needs more data: Luminance least complete at 4% of its goal" {
		t.Fatal(l.Text())
	}
}

func TestLeastCountsPlateauAsComplete(t *testing.T) {
	t.Parallel()
	gs := []FilterGoal{measuredGoal("Luminance", 0.3, true), measuredGoal("Red", 0.6, false)}
	if *gs[0].GoalProgress != 1 {
		t.Fatalf("plateau-done filter %+v", gs[0].GoalProgress)
	}
	l := leastOfGoals("x", gs)
	if l.Filter != "Red" || math.Abs(*l.Progress-0.6) > 1e-9 {
		t.Fatalf("%+v", l)
	}
	l = leastOfGoals("x", gs[:1])
	if !l.Enough || *l.Progress != 1 || l.Text() != "enough data in every filter" {
		t.Fatalf("%+v", l)
	}
}

func TestLeastUnmeasuredRanksBelowMeasured(t *testing.T) {
	t.Parallel()
	collecting := FilterGoal{Filter: "O-III", Goal: &goals.Goal{}, Status: StatusNoMaster, Readiness: &goals.Readiness{State: goals.StateCollecting, StackSubs: 2, MinSubs: 8, Open: true}}
	finishFilter(&collecting)
	noGoal := FilterGoal{Filter: "S-II", Status: StatusMeasured}
	finishFilter(&noGoal)
	if collecting.GoalProgress != nil || collecting.NotMeasured != CollectingFirst || noGoal.NotMeasured != NoGoalReason {
		t.Fatalf("%+v %+v", collecting, noGoal)
	}
	l := leastOfGoals("x", []FilterGoal{measuredGoal(filterHa, 0.01, false), collecting, noGoal})
	if l.Filter != "O-III" || l.Progress != nil || l.NotMeasured != CollectingFirst || l.Unmeasured != 2 {
		t.Fatalf("%+v", l)
	}
	if l.Text() != "needs more data: O-III not measured yet (collecting subs for the first measurement)" {
		t.Fatal(l.Text())
	}
	if e := leastOfGoals("x", nil); e.Enough || e.Text() != NoPlansReason {
		t.Fatalf("%+v", e)
	}
}

func TestLeastOfTargetsSkipsInactive(t *testing.T) {
	t.Parallel()
	a := Target{Name: "a", Active: true, Least: leastOfGoals("a", []FilterGoal{measuredGoal("L", 0.5, false)})}
	b := Target{Name: "b", Active: true, Least: leastOfGoals("b", []FilterGoal{measuredGoal("L", 0.2, false), measuredGoal("R", 1, true)})}
	c := Target{Name: "c", Active: false, Least: leastOfGoals("c", []FilterGoal{measuredGoal("L", 0.01, false)})}
	l := leastOfTargets([]Target{a, b, c})
	if l.Target != "b" || math.Abs(*l.Progress-0.2) > 1e-9 || l.Filters != 3 || l.Enough {
		t.Fatalf("%+v", l)
	}
	if l := leastOfTargets([]Target{c}); l.Filters != 0 || l.Enough || l.NotMeasured != NoPlansReason {
		t.Fatalf("%+v", l)
	}
}

func TestNotMeasuredReasonPrefersCollectingOverAFailedFirstTry(t *testing.T) {
	t.Parallel()
	g := FilterGoal{Filter: "L", Goal: &goals.Goal{}, Status: StatusFailed, Error: "insufficient data", Readiness: &goals.Readiness{State: goals.StateCollecting, StackSubs: 3, MinSubs: 8}}
	if r := notMeasuredReason(g); r != CollectingFirst {
		t.Fatal(r)
	}
	g.Readiness.StackSubs = 9
	if r := notMeasuredReason(g); r != FailedReason+": insufficient data" {
		t.Fatal(r)
	}
}

func TestCollectingIsNeverEnoughEvenWhenThePluginPaused(t *testing.T) {
	t.Parallel()
	g := FilterGoal{Filter: filterHa, Goal: &goals.Goal{}, Status: StatusFailed, Error: "insufficient data", Enough: true,
		Readiness: &goals.Readiness{State: goals.StateCollecting, StackSubs: 12, MinSubs: 8}}
	finishFilter(&g)
	if g.Enough || g.GoalProgress != nil || g.NotMeasured != FailedReason+": insufficient data" {
		t.Fatalf("%+v", g)
	}
}

func TestLeastOfTargetsSkipsTargetsWithoutEnabledPlans(t *testing.T) {
	t.Parallel()
	done := Target{Name: "done", Active: true, Least: leastOfGoals("done", []FilterGoal{measuredGoal("L", 1, true)})}
	empty := Target{Name: "empty", Active: true, Least: leastOfGoals("empty", nil)}
	l := leastOfTargets([]Target{empty, done})
	if l.Target != "done" || !l.Enough || l.Progress == nil || *l.Progress != 1 || l.Filters != 1 || l.Filter != "L" {
		t.Fatalf("%+v", l)
	}
	if l := leastOfTargets([]Target{empty}); l.Filters != 0 || l.Enough || l.NotMeasured != NoPlansReason {
		t.Fatalf("%+v", l)
	}
}
