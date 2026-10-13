package cmd

import (
	"sync/atomic"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
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
	// scheduling runs the global scheduling settings.
	scheduling func()

	// ready is set once every kick is assigned; commands sent earlier
	// kick nothing.
	ready atomic.Bool
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
	k.ready.Store(true)
}

// onCommand hears every scheduler command as it is sent, applied or
// refused.
func (k *kicks) onCommand(r schedcmd.Record) {
	if !k.ready.Load() {
		return
	}
	if schedauto.Affects(r) {
		call(k.scheduling)
	}
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
	call(k.moonKeep, k.subLength, k.scheduling)
}
