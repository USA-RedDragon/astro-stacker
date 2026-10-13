package cmd

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/schedwatch"
)

func TestKicksFollowEventsAndPlanChanges(t *testing.T) {
	t.Parallel()
	var measured []string
	k := &kicks{goals: func(object, filter string) { measured = append(measured, object+"/"+filter) }}
	broker := events.NewBroker()
	w := &schedwatch.Watcher{}
	k.listen(broker, w)
	broker.Publish(events.Event{Type: events.TypeMaster, Object: "M31", Filter: "H-a"})
	broker.Publish(events.Event{Type: events.TypePreview, Object: "M33", Filter: "H-a"})
	for _, fn := range w.OnPlans {
		fn()
	}
	if len(measured) != 2 || measured[0] != "M31/H-a" || measured[1] != "/" {
		t.Fatalf("a master update measures that master, a plan change every master: %v", measured)
	}
	(&kicks{}).listen(broker, w)
	broker.Publish(events.Event{Type: events.TypeMaster, Object: "M31"})
	for _, fn := range w.OnPlans {
		fn()
	}
}
