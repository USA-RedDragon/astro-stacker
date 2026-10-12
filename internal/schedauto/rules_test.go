package schedauto_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	ditherOneToTwo = "1>2"
	entity         = "project"
	profile        = "prof"
)

func site() sky.Site { return sky.Site{Latitude: 31.546944, Longitude: -99.382222} }

func h(v float64) *float64 { return &v }

func TestSwitchFloorKeepsSwitchingUnderTheShare(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		per  float64
		want int
	}{{4.37, 25}, {4.22, 25}, {5.35, 35}, {3.0, 20}, {0, 25}, {-1, 25}} {
		got := schedauto.SwitchFloor(c.per)
		if got != c.want {
			t.Errorf("SwitchFloor(%v) = %d, want %d", c.per, got, c.want)
		}
		per := c.per
		if per <= 0 {
			per = schedauto.DefaultSwitchMinutes
		}
		if share := per / (float64(got) + per); share > schedauto.MaxSwitchShare {
			t.Errorf("a %d min stint with a %v min switch spends %.3f switching", got, per, share)
		}
	}
}

func TestMinTimeIsTheSwitchingFloorOrTheWholeWindow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name             string
		tonight, typical *float64
		want             int
	}{
		{"long window", h(8), h(9), 25},
		{"tonight short", h(22.0 / 60), h(3), 20},
		{"typical short", h(4), h(13.0 / 60), 10},
		{"at least one step", h(3.0 / 60), nil, schedauto.MinTimeStep},
		{"not up tonight uses typical", nil, h(2), 25},
	} {
		m := schedauto.MinTimeFor(schedauto.Window{Tonight: c.tonight, Typical: c.typical}, 25)
		if m == nil || m.Minutes != c.want || m.Basis == "" || m.Window == nil {
			t.Errorf("%s: %+v, want %d", c.name, m, c.want)
		}
	}
	if m := schedauto.MinTimeFor(schedauto.Window{}, 25); m == nil || m.Minutes != 25 || m.Window != nil {
		t.Errorf("no window: %+v", m)
	}
}

func TestMinAltitudeIsTheHardLimit(t *testing.T) {
	t.Parallel()
	if m := schedauto.MinAltitudeFor(false); m.Degrees != schedauto.HardMinAltitude || m.Horizon {
		t.Errorf("no horizon: %+v", m)
	}
	if m := schedauto.MinAltitudeFor(true); m.Degrees != 0 || !m.Horizon {
		t.Errorf("horizon: %+v", m)
	}
}

func TestMeasureOverhead(t *testing.T) {
	t.Parallel()
	night := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	l := func(obj string, at time.Time) schedauto.Light {
		return schedauto.Light{Object: obj, Start: at, Exposure: 300}
	}
	lights := []schedauto.Light{
		l("A", night), l("A", night.Add(310*time.Second)),
		l("B", night.Add(610*time.Second+4*time.Minute)),
		l("B", night.Add(610*time.Second+4*time.Minute)),
		l("C", night.Add(910*time.Second+4*time.Minute+30*time.Minute)),
		l("D", night.Add(1210*time.Second+4*time.Minute+30*time.Minute+5*time.Minute)),
		l("A", night.Add(-24*time.Hour)), l("B", night.Add(-24*time.Hour+306*time.Second)),
		l("Z", night.Add(-400*24*time.Hour)), l("Y", night.Add(-400*24*time.Hour+302*time.Second)),
	}
	o := schedauto.MeasureOverhead(lights, site().Longitude, night.Add(6*time.Hour))
	if o.Switches != 3 || o.LongerGaps != 1 || o.Nights != 2 {
		t.Fatalf("%+v", o)
	}
	if o.MedianMinutes != 4 || o.MeanMinutes != 3.03 || o.P90Minutes != 5 {
		t.Errorf("switch minutes %+v", o)
	}
	if len(o.Recent) != 2 || o.Recent[0].Night != "2026-10-11" || o.Recent[0].Switches != 2 || o.Recent[0].SwitchMinutes != 9 {
		t.Fatalf("recent %+v", o.Recent)
	}
	if got := o.Recent[0].Share; got <= 0 || got > 0.2 {
		t.Errorf("share %v", got)
	}
}

func TestFixedWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC)
	n := schedauto.NightsFrom(site(), now, 3)
	if n.First != "2026-10-12" {
		t.Fatalf("tonight %s", n.First)
	}
	polar := n.FixedWindow(37.95, 89.26, mosaics.Flat(20))
	if polar.Tonight == nil || *polar.Tonight < 9 || polar.Nights != 3 || polar.Typical == nil {
		t.Errorf("Polaris is up all night: %+v", polar)
	}
	if w := n.FixedWindow(37.95, 89.26, mosaics.Flat(40)); w.Tonight != nil || w.Typical != nil || w.Nights != 0 {
		t.Errorf("Polaris never reaches 40° here: %+v", w)
	}
	m31 := n.FixedWindow(10.68, 41.27, mosaics.Flat(20))
	wall := mosaics.Horizon{Step: 90, Altitudes: []float64{60, 60, 60, 60}}
	walled := n.FixedWindow(10.68, 41.27, mosaics.Limit{Min: 20, Horizon: &wall})
	if m31.Tonight == nil || walled.Tonight == nil || *walled.Tonight >= *m31.Tonight {
		t.Errorf("a 60° horizon shortens M31's window: %v vs %v", walled.Tonight, m31.Tonight)
	}
}

func project(state string) planning.Project {
	i, b, f := func(v int) *int { return &v }, func(v bool) *bool { return &v }, func(v float64) *float64 { return &v }
	ra := 10.68 / 15
	return planning.Project{ID: 7, GUID: "p-guid", Name: "Andromeda", State: state, MinimumTime: 90, MinimumAltitude: 25, Grader: true,
		DitherEvery: i(1), FilterSwitchFrequency: i(1), FlatsHandling: i(1), SmartExposureOrder: b(false), MeridianWindow: i(0),
		UseCustomHorizon: b(false), HorizonOffset: f(0), MaximumAltitude: f(0), ProfileID: profile,
		Targets: []planning.Target{
			{ID: 1, GUID: "t1", Name: "Panel 1", Active: true, RAHours: &ra, Dec: f(41.27), Plans: []planning.Plan{{Filter: "Luminance", Enabled: true}}},
			{ID: 2, GUID: "t2", Name: "Panel 2", Active: false, RAHours: &ra, Dec: f(-20), Plans: []planning.Plan{{Filter: "H-a", Enabled: true}}},
		}}
}

func inputs() schedauto.Inputs {
	return schedauto.Inputs{Global: schedglobal.Defaults(), SwitchFloor: 25,
		Nights:  schedauto.NightsFrom(site(), time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC), 5),
		Horizon: nil}
}

func changed(r schedauto.ProjectResult) map[string]string {
	out := map[string]string{}
	for _, c := range r.Changes {
		out[c.Field] = string(c.Before) + ">" + string(c.After)
	}
	return out
}

func TestPlanMakesAProjectEqualToTheGlobalAndAutomaticValues(t *testing.T) {
	t.Parallel()
	r := schedauto.Plan(project("Active"), inputs())
	c := changed(r)
	if c[schedcmd.FieldDitherEvery] != ditherOneToTwo || c[schedauto.FieldMinimumAltitude] != "25>15" || c[schedauto.FieldMinimumTime] != "90>25" {
		t.Errorf("changes %v", c)
	}
	if _, ok := c[schedcmd.FieldFlatsHandling]; ok {
		t.Errorf("flats already every day: %v", c)
	}
	if r.MinAltitude.Degrees != 15 {
		t.Errorf("the hard limit: %+v", r.MinAltitude)
	}
	if r.MinTime == nil || r.MinTimeTarget != "Panel 1" || len(r.Targets) != 2 || r.Targets[1].MinTime == nil {
		t.Errorf("minimum time from the active panel only: %+v %s", r.MinTime, r.MinTimeTarget)
	}
	e := r.Edit()
	if e == nil || e.Entity != entity || e.ID != 7 || len(e.Changes) != len(r.Changes) {
		t.Fatalf("edit %+v", e)
	}
	raw, err := json.Marshal(schedcmd.TemplateBatchPayload{Items: []schedcmd.EditPayload{*e}})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := schedcmd.Default().Get(schedcmd.KindProjectBatchEdit)
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(raw); err != nil {
		t.Errorf("the batch is a valid project.batchedit: %v", err)
	}
}

func TestPlanLeavesClosedProjectsAndSkyEventTimesAlone(t *testing.T) {
	t.Parallel()
	if r := schedauto.Plan(project("Closed"), inputs()); len(r.Changes) != 0 || r.Left == "" || r.Edit() != nil {
		t.Errorf("closed project changed: %+v", r.Changes)
	}
	in := inputs()
	in.Events = map[string]bool{"p-guid": true}
	p := project("Active")
	p.GUID = "P-GUID"
	r := schedauto.Plan(p, in)
	c := changed(r)
	if !r.Event || c[schedauto.FieldMinimumTime] != "" || c[schedauto.FieldMinimumAltitude] != "" || c[schedcmd.FieldDitherEvery] != ditherOneToTwo {
		t.Errorf("sky event keeps its own times and altitude but takes the global set: %v", c)
	}
}

func TestPlanUsesTheObservatoryHorizonOfTheProjectsProfile(t *testing.T) {
	t.Parallel()
	in := inputs()
	wall := mosaics.Horizon{Step: 90, Altitudes: []float64{30, 30, 30, 30}}
	in.Horizon = &schedauto.Horizon{ProfileID: "PROF", Custom: true, Horizon: &wall}
	r := schedauto.Plan(project("Active"), in)
	if changed(r)[schedcmd.FieldUseCustomHorizon] != "false>true" || !r.MinAltitude.Horizon || changed(r)[schedauto.FieldMinimumAltitude] != "25>0" {
		t.Errorf("custom horizon on: %v", changed(r))
	}
	in.Horizon.ProfileID = "other"
	if r := schedauto.Plan(project("Active"), in); changed(r)[schedcmd.FieldUseCustomHorizon] != "" {
		t.Errorf("another profile's horizon is not this project's: %v", changed(r))
	}
	on := project("Active")
	b := true
	on.UseCustomHorizon = &b
	if r := schedauto.Plan(on, inputs()); changed(r)[schedcmd.FieldUseCustomHorizon] != "" {
		t.Errorf("with no horizon report a project keeps its setting: %v", changed(r))
	}
	in.Horizon = &schedauto.Horizon{ProfileID: profile}
	if r := schedauto.Plan(on, in); changed(r)[schedcmd.FieldUseCustomHorizon] != "true>false" {
		t.Errorf("a profile with no custom horizon turns it off: %v", changed(r))
	}
	in.Horizon = &schedauto.Horizon{ProfileID: profile, Custom: true}
	if r := schedauto.Plan(on, in); changed(r)[schedcmd.FieldUseCustomHorizon] != "" {
		t.Errorf("an unreadable custom horizon changes nothing: %v", changed(r))
	}
}

func TestPlanLeavesMaximumAltitudeBelowTheMinimumAlone(t *testing.T) {
	t.Parallel()
	in := inputs()
	in.Global.MaximumAltitude = 12
	r := schedauto.Plan(project("Active"), in)
	if _, ok := changed(r)[schedcmd.FieldMaximumAltitude]; ok || len(r.Notes) != 1 {
		t.Errorf("a maximum at or below the minimum is not sent: %v %v", changed(r), r.Notes)
	}
	in.Global.MaximumAltitude = 80
	if r := schedauto.Plan(project("Active"), in); changed(r)[schedcmd.FieldMaximumAltitude] != "0>80" {
		t.Errorf("a maximum above the minimum is sent: %v", changed(r))
	}
	raw, err := json.Marshal(schedcmd.EditPayload{Entity: entity, ID: 7, Name: "x", Changes: schedauto.Plan(project("Active"), in).Changes})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := schedcmd.Default().Get(schedcmd.KindProjectEdit)
	if err != nil || spec.Validate(raw) != nil {
		t.Errorf("valid project.edit: %v", err)
	}
}

func TestBatchLeavesBusyProjectsForLater(t *testing.T) {
	t.Parallel()
	a := schedauto.Plan(project("Active"), inputs())
	b := a
	b.ID, b.Name = 8, "Busy"
	same := a
	same.ID, same.Changes = 9, nil
	noGUID := a
	noGUID.ID, noGUID.Name, noGUID.GUID = 10, "No GUID", ""
	edits, left := schedauto.Edits([]schedauto.ProjectResult{a, b, same, noGUID}, map[int64]bool{8: true})
	if len(edits) != 1 || edits[0].ID != 7 || len(left) != 2 || left[0] != "Busy" || left[1] != "No GUID" {
		t.Errorf("edits %+v left %v", edits, left)
	}
}

func TestOverridesAndProjection(t *testing.T) {
	t.Parallel()
	r := schedauto.Plan(project("Active"), inputs())
	ov := schedauto.Overrides([]schedauto.ProjectResult{r})
	if len(ov) == 0 || ov[0].Entity != entity || ov[0].ID != 7 {
		t.Fatalf("overrides %+v", ov)
	}
	raw := json.RawMessage(`{"blocks":[
		{"start":"2026-10-13T00:00:00Z","end":"2026-10-13T00:30:00Z","wait":true,"target_id":6},
		{"start":"2026-10-13T00:30:00Z","end":"2026-10-13T02:30:00Z","target_id":1},
		{"start":"2026-10-13T02:30:00Z","end":"2026-10-13T04:30:00Z","target_id":2},
		{"start":"2026-10-13T04:30:00Z","end":"2026-10-13T04:40:00Z","wait":true,"target_id":0},
		{"start":"2026-10-13T04:40:00Z","end":"2026-10-13T06:30:00Z","target_id":2},
		{"start":"2026-10-13T06:30:00Z","end":"2026-10-13T08:30:00Z","target_id":1}]}`)
	p, err := schedauto.ParseProjection(raw, 4)
	if err != nil || p.Switches != 2 || p.Targets != 2 || p.ImagingHours != 7.83 || p.SwitchMinutes != 8 || p.Share != 0.017 {
		t.Errorf("%+v %v", p, err)
	}
	if _, err := schedauto.ParseProjection(json.RawMessage(`[`), 4); err == nil {
		t.Error("bad JSON")
	}
}
