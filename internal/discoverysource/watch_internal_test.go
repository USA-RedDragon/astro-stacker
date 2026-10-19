package discoverysource

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestWatchingCounts(t *testing.T) {
	t.Parallel()
	vet := func(conf, pending string) *app.DiscoveryVet {
		return &app.DiscoveryVet{Suggested: app.VetReal, Confidence: conf, Pending: pending}
	}
	change := func(id int, v *app.DiscoveryVet, w *app.DiscoveryWatch) Summary {
		return Summary{Source: app.DiscoverySource{ID: id, Kind: app.DiscoveryKindChange}, Class: discoveries.ClassNewVariable, Vet: v, Watch: w}
	}
	now := time.Now()
	list := []Summary{
		change(1, vet("low", ""), nil),
		change(2, vet("low", "ATLAS"), nil),
		change(3, vet("medium", ""), nil),
		change(4, vet("low", "ATLAS"), &app.DiscoveryWatch{SourceID: 4}),
		change(5, vet("low", ""), &app.DiscoveryWatch{SourceID: 5, EndedAt: &now, EndReason: app.WatchEndFades}),
		change(6, nil, nil),
	}
	c := countOf(list)
	if c.Watching != 2 || c.Look != 4 {
		t.Errorf("counts %+v, want 1 and 4 in Watching and 2, 3, 5 and 6 in Needs a look", c)
	}
	for _, s := range list {
		if s.In(ViewWatching) == s.In(ViewLook) {
			t.Errorf("source %d is in both or neither of Watching and Needs a look", s.Source.ID)
		}
	}
}
