package observatory_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
)

const pluginVersion = "5.8.2.210"

func TestPreviewInputs(t *testing.T) {
	t.Parallel()
	m := observatory.NewMonitor(observatory.NewClient("", ""), nil, nil)
	if got := m.PreviewInputs(); got != "" {
		t.Fatalf("no status: %q", got)
	}
	m.SetStatus(observatory.Status{Version: pluginVersion, State: "imaging"}, true)
	base := m.PreviewInputs()
	m.SetStatus(observatory.Status{Version: pluginVersion, State: "waiting"}, true)
	if got := m.PreviewInputs(); got != base {
		t.Fatalf("state changed the inputs: %q vs %q", got, base)
	}
	m.SetStatus(observatory.Status{Version: pluginVersion, Skips: []observatory.Skip{{Scope: "target", TargetID: 13}}}, true)
	if got := m.PreviewInputs(); got == base {
		t.Fatalf("a skip did not change the inputs: %q", got)
	}
}

func TestPreviewInputsFollowTheRunningExposure(t *testing.T) {
	t.Parallel()
	m := observatory.NewMonitor(observatory.NewClient("", ""), nil, nil)
	started := time.Date(2026, 10, 11, 6, 28, 44, 0, time.UTC)
	status := func(state string, target int64, at *time.Time) observatory.Status {
		s := observatory.Status{Version: pluginVersion, State: state, Target: &observatory.Target{TargetID: target}}
		if at != nil {
			s.Exposure = &observatory.Exposure{PlanID: 774, Filter: "Green", Seconds: 600, StartedAt: at}
		}
		return s
	}
	inputs := func(s observatory.Status) string {
		m.SetStatus(s, true)
		return m.PreviewInputs()
	}
	first := inputs(status("imaging", 130, &started))
	if got := inputs(status("imaging", 130, &started)); got != first {
		t.Fatalf("the same exposure changed the inputs: %q vs %q", got, first)
	}
	next := started.Add(10 * time.Minute)
	if got := inputs(status("imaging", 130, &next)); got == first {
		t.Fatalf("a new exposure did not change the inputs: %q", got)
	}
	between := inputs(status("imaging", 130, nil))
	if between == first {
		t.Fatalf("the end of the exposure did not change the inputs: %q", between)
	}
	if got := inputs(status("imaging", 128, nil)); got == between {
		t.Fatalf("a new target did not change the inputs: %q", got)
	}
	if got, idle := inputs(status("waiting", 130, &started)), inputs(observatory.Status{Version: pluginVersion, State: "waiting"}); got != idle {
		t.Fatalf("a target outside imaging changed the inputs: %q vs %q", got, idle)
	}
}
