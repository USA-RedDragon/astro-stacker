package stacking

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
)

// With nothing in hand, a drained pipeline's status reporting stops at
// once instead of holding Run until the drain's limit.
func TestDrainStopsStatusReporting(t *testing.T) {
	t.Parallel()
	p := &Pipeline{drain: make(chan struct{}), working: map[string]*events.Worker{}, Events: events.NewBroker()}
	done := make(chan struct{})
	go func() {
		p.reportStatus(context.Background())
		close(done)
	}()
	p.Drain()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("status reporting kept running after a drain")
	}
}
