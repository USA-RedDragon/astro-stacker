package observatory_test

import (
	"testing"

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
