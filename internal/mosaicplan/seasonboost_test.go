package mosaicplan_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaicplan"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func fp(v float64) *float64 { return &v }

func TestSeasonPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		needed, available, want float64
	}{
		{10, 10, 0.5},
		{30, 10, 0.75},
		{10, 30, 0.25},
		{10, 0, 1},
		{0, 10, 0},
		{-1, 10, 0},
		{math.NaN(), 10, 0},
		{math.Inf(1), 10, 0},
		{5, -3, 1},
	}
	for _, c := range cases {
		if got := mosaicplan.SeasonPriority(c.needed, c.available); !near(got, c.want) {
			t.Errorf("SeasonPriority(%v, %v) = %v, want %v", c.needed, c.available, got, c.want)
		}
	}
}

func TestClearShare(t *testing.T) {
	t.Parallel()
	months := make([]mosaicplan.MonthNights, 12)
	months[0].Nights = fp(31)
	months[1].Nights = fp(7)
	months[3].Nights = fp(60)
	got := mosaicplan.ClearShare(months)
	if !near(got[0], 1) || !near(got[1], 0.25) || !near(got[3], 1) {
		t.Fatalf("known months %v", got)
	}
	if want := 2.25 / 3; !near(got[2], want) || !near(got[11], want) {
		t.Errorf("months without history should take the mean of known months %v, got %v", want, got)
	}
	for _, v := range mosaicplan.ClearShare(nil) {
		if v != 1 {
			t.Fatalf("no history should count every night: %v", v)
		}
	}
}

const (
	filterOIII = "O-III"
	twoWeeks   = "2026-10-23"
	longEnd    = "2027-04-28"
)

func testSite() mosaics.Site { return mosaics.Site{Lat: 35, Lon: -110} }

func testNow() time.Time { return time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC) }

func allClear() [12]float64 {
	var c [12]float64
	for i := range c {
		c[i] = 1
	}
	return c
}

func TestSeasonHours(t *testing.T) {
	t.Parallel()
	polar := mosaics.Point{RA: 0, Dec: 85}
	all := allClear()
	if _, in := mosaicplan.SeasonHours(nil, testSite(), polar, mosaics.Flat(20), 60, all, testNow()); in {
		t.Error("no season row should be out of season")
	}
	if _, in := mosaicplan.SeasonHours(&planning.Season{OutOfSeason: true, NightsLeft: 10}, testSite(), polar, mosaics.Flat(20), 60, all, testNow()); in {
		t.Error("out of season")
	}
	if _, in := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 0}, testSite(), polar, mosaics.Flat(20), 60, all, testNow()); in {
		t.Error("no nights left")
	}
	short, in := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 14, SeasonEnd: twoWeeks}, testSite(), polar, mosaics.Flat(20), 60, all, testNow())
	if !in || short < 14*8 || short > 14*11 {
		t.Fatalf("two weeks of a circumpolar target in October: %v h", short)
	}
	long, _ := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 60, SeasonEnd: "2026-12-09"}, testSite(), polar, mosaics.Flat(20), 60, all, testNow())
	if long <= 3*short {
		t.Errorf("a longer season should hold more hours: %v vs %v", long, short)
	}
	byCount, _ := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 14}, testSite(), polar, mosaics.Flat(20), 60, all, testNow())
	if !near(byCount, short) {
		t.Errorf("without a season end the night count sets the span: %v vs %v", byCount, short)
	}
	var half [12]float64
	for i := range half {
		half[i] = 0.5
	}
	weather, _ := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 14, SeasonEnd: twoWeeks}, testSite(), polar, mosaics.Flat(20), 60, half, testNow())
	if !near(weather, short/2) {
		t.Errorf("half the nights clear should halve the hours: %v vs %v", weather, short)
	}
	tooShort, _ := mosaicplan.SeasonHours(&planning.Season{NightsLeft: 14, SeasonEnd: twoWeeks}, testSite(), polar, mosaics.Flat(20), 24*60, all, testNow())
	if tooShort != 0 {
		t.Errorf("nights shorter than the minimum time should not count: %v", tooShort)
	}
}

func TestTargetHoursNeeded(t *testing.T) {
	t.Parallel()
	tgt := planning.Target{
		Plans: []planning.Plan{
			{Filter: "Luminance", Exposure: 300, Desired: 20, Accepted: 10, Acquired: 15, Enabled: true},
			{Filter: "Red", Exposure: 300, Desired: 20, Accepted: 20, Enabled: true},
			{Filter: "H-a", Exposure: 600, Desired: 12, Accepted: 2, Enabled: true},
			{Filter: filterOIII, Exposure: 600, Desired: 30, Accepted: 0, Enabled: true},
			{Filter: "S-II", Exposure: 600, Desired: 30, Accepted: 0, Enabled: true},
			{Filter: "Blue", Exposure: 300, Desired: 100, Enabled: false},
			{Filter: "Green", Exposure: 300, Desired: 100, Enabled: true, Complete: true},
		},
		Goals: []planning.FilterGoal{
			{Filter: "Luminance", Readiness: &goals.Readiness{State: goals.StateMeasured}, Progress: &goals.Progress{HoursNeeded: 6}},
			{Filter: "Red", Readiness: &goals.Readiness{State: goals.StateMeasured}, Progress: &goals.Progress{Done: true, HoursNeeded: 0}},
			{Filter: "H-a", Readiness: &goals.Readiness{State: goals.StateCollecting, StackSubs: 2, MinSubs: 8}},
			{Filter: filterOIII, Readiness: &goals.Readiness{State: goals.StateUnmeasurable, StackSubs: 10, SubLimit: 32}},
		},
	}
	want := 6/0.5 + 10*600.0/3600 + 22*600.0/3600 + 30*600.0/3600
	if got := mosaicplan.TargetHoursNeeded(tgt, true, 0.5); !near(got, want) {
		t.Errorf("graded: %v want %v", got, want)
	}
	tgt.Goals[2].Readiness.MinSubs = 20
	want = 6/0.5 + 18*600.0/3600 + 22*600.0/3600 + 30*600.0/3600
	if got := mosaicplan.TargetHoursNeeded(tgt, true, 0.5); !near(got, want) {
		t.Errorf("collecting should take the larger of the plan count and the measuring subs: %v want %v", got, want)
	}
	tgt.Goals = nil
	want = 10*300.0/3600 + 10*600.0/3600 + 30*600.0/3600 + 30*600.0/3600
	if got := mosaicplan.TargetHoursNeeded(tgt, true, 0); !near(got, want) {
		t.Errorf("no goals: %v want %v", got, want)
	}
	want = 5*300.0/3600 + 20*300.0/3600 + 12*600.0/3600 + 30*600.0/3600 + 30*600.0/3600
	if got := mosaicplan.TargetHoursNeeded(tgt, false, 0); !near(got, want) {
		t.Errorf("ungraded counts acquired: %v want %v", got, want)
	}
}

func TestSeasonBoostsFor(t *testing.T) {
	t.Parallel()
	plans := func(desired int) []planning.Plan {
		return []planning.Plan{{Filter: "L", Exposure: 3600, Desired: desired, Enabled: true}}
	}
	target := func(guid string, ra float64, season *planning.Season, desired int) planning.Target {
		return planning.Target{GUID: guid, Name: guid, Active: true, RAHours: fp(ra), Dec: fp(85), Season: season, Plans: plans(desired)}
	}
	projects := []planning.Project{
		{Name: "single", State: activeState, MinimumAltitude: 20, MinimumTime: 60, Targets: []planning.Target{
			target("short", 0, &planning.Season{NightsLeft: 10, SeasonEnd: "2026-10-20"}, 40),
		}},
		{Name: "mosaic", State: activeState, IsMosaic: true, MinimumAltitude: 20, MinimumTime: 60, Targets: []planning.Target{
			target("long", 0, &planning.Season{NightsLeft: 200, SeasonEnd: longEnd}, 40),
			target("long-small", 0, &planning.Season{NightsLeft: 200, SeasonEnd: longEnd}, 4),
			target("out", 0, &planning.Season{OutOfSeason: true}, 40),
			target("done", 0, &planning.Season{NightsLeft: 200, SeasonEnd: longEnd}, 0),
			{GUID: "inactive", Active: false, RAHours: fp(0), Dec: fp(85), Season: &planning.Season{NightsLeft: 10}, Plans: plans(40)},
		}},
		{Name: "closed", State: "Closed", Targets: []planning.Target{target("closed", 0, &planning.Season{NightsLeft: 10}, 40)}},
	}
	got := map[string]mosaicplan.TargetSeasonBoost{}
	for _, b := range mosaicplan.SeasonBoostsFor(projects, testSite(), nil, allClear(), 1, testNow()) {
		got[b.GUID] = b
	}
	if len(got) != 5 {
		t.Fatalf("active targets of active projects only: %v", got)
	}
	if got["out"].Boost != 0 || got["done"].Boost != 0 {
		t.Errorf("out of season and finished targets get no boost: %v %v", got["out"], got["done"])
	}
	s, l, ls := got["short"], got["long"], got["long-small"]
	if !(s.Boost > l.Boost && l.Boost > ls.Boost && ls.Boost > 0) {
		t.Errorf("boost should rank by need against the season left, across projects: short %v long %v small %v", s.Boost, l.Boost, ls.Boost)
	}
	if !near(s.Boost, s.Needed/(s.Needed+s.Available)) || s.Needed != 40 {
		t.Errorf("short %+v", s)
	}
	if l.Boost >= 0.5 || s.Boost <= 0.2 {
		t.Errorf("absolute scale: short %v long %v", s.Boost, l.Boost)
	}
}
