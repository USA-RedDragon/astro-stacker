package cmd

import (
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedwatch"
)

type kickLog struct{ heard []string }

func (l *kickLog) fn(name string) func() { return func() { l.heard = append(l.heard, name) } }

func (l *kickLog) take() []string {
	out := l.heard
	l.heard = nil
	slices.Sort(out)
	return out
}

func newKicks(l *kickLog) *kicks {
	return &kicks{
		goals:        func(object, filter string) { l.heard = append(l.heard, "goals:"+object+"/"+filter) },
		moonKeep:     l.fn("moonKeep"),
		subLength:    l.fn("subLength"),
		scheduling:   l.fn("scheduling"),
		verdicts:     l.fn("verdicts"),
		mosaics:      l.fn("mosaics"),
		goalSwitch:   l.fn("goalSwitch"),
		publicFrames: l.fn("publicFrames"),
	}
}

func TestKicksFollowEventsAndPlanChanges(t *testing.T) {
	t.Parallel()
	l := &kickLog{}
	broker := events.NewBroker()
	w := &schedwatch.Watcher{}
	k := newKicks(l)
	k.onCommand(schedcmd.Record{Kind: schedcmd.KindProjectCreate})
	k.onMeasured("M31", "Luminance")
	if got := l.take(); got != nil {
		t.Errorf("a command before every kick is assigned kicked %v", got)
	}
	k.listen(broker, w)
	plans := func() {
		for _, fn := range w.OnPlans {
			fn()
		}
	}
	cases := []struct {
		name string
		do   func()
		want []string
	}{
		{"master updated", func() { broker.Publish(events.Event{Type: events.TypeMaster, Object: "M31", Filter: "H-a"}) },
			[]string{"goals:M31/H-a", "mosaics"}},
		{"subs triaged", func() { broker.Publish(events.Event{Type: events.TypeSubs, Object: "M31", Filter: "H-a"}) },
			[]string{"moonKeep", "publicFrames", "subLength", "verdicts"}},
		{"frames indexed", func() { broker.Publish(events.Event{Type: events.TypeFrames}) }, []string{"mosaics"}},
		{"sync arrived", func() {
			for _, fn := range w.OnSync {
				fn()
			}
		}, []string{"verdicts"}},
		{"preview rendered", func() { broker.Publish(events.Event{Type: events.TypePreview, Object: "M31"}) }, nil},
		{"plans changed", plans, []string{"goalSwitch", "goals:/", "moonKeep", "scheduling", "subLength"}},
		{"goal measured", func() { k.onMeasured("M31", "Luminance") }, []string{"goalSwitch"}},
		{"project drafted", func() { k.onCommand(schedcmd.Record{Kind: schedcmd.KindProjectCreate}) }, []string{"scheduling"}},
		{"global settings saved", func() { k.onCommand(schedcmd.Record{Kind: schedcmd.KindSchedulingGlobal}) }, []string{"scheduling"}},
		{"target skipped", func() { k.onCommand(schedcmd.Record{Kind: schedcmd.KindSkip}) }, nil},
	}
	for _, c := range cases {
		c.do()
		if got := l.take(); !slices.Equal(got, c.want) {
			t.Errorf("%s kicked %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNilKicksAreSkipped(t *testing.T) {
	t.Parallel()
	broker := events.NewBroker()
	w := &schedwatch.Watcher{}
	(&kicks{}).listen(broker, w)
	for _, typ := range []string{events.TypeMaster, events.TypeSubs, events.TypeFrames, events.TypeMosaic} {
		broker.Publish(events.Event{Type: typ, Object: "M31"})
	}
	for _, fn := range append(w.OnPlans, w.OnSync...) {
		fn()
	}
	(&kicks{}).onCommand(schedcmd.Record{Kind: schedcmd.KindProjectCreate})
}
