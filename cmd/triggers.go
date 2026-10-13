package cmd

import (
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/schedwatch"
)

// kicks start work as soon as its input changes. A nil kick is skipped.
type kicks struct {
	// goals measures a master's goal progress; with no object, every
	// master's, as when goals change.
	goals func(object, filter string)
	// moonKeep refits the Moon and altitude kept rates.
	moonKeep func()
	// subLength works the sub lengths out again.
	subLength func()
}

func call(fns ...func()) {
	for _, fn := range fns {
		if fn != nil {
			fn()
		}
	}
}

// listen wires published events and scheduler database changes to the
// kicks.
func (k *kicks) listen(broker *events.Broker, w *schedwatch.Watcher) {
	broker.Listen(k.onEvent)
	w.OnPlans = append(w.OnPlans, k.onPlans)
}

func (k *kicks) onEvent(e events.Event) {
	switch e.Type {
	case events.TypeMaster:
		if k.goals != nil {
			k.goals(e.Object, e.Filter)
		}
	case events.TypeSubs:
		call(k.moonKeep, k.subLength)
	}
}

func (k *kicks) onPlans() {
	if k.goals != nil {
		k.goals("", "")
	}
	call(k.moonKeep, k.subLength)
}
