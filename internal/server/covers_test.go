package server

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	objectM31   = "M31"
	objectOrion = "Orion"
	objectVeil  = "Veil"
	filterBlue  = "Blue"
	filterHa    = "H-a"
)

func TestMonoCoversPreferNebulaFilters(t *testing.T) {
	t.Parallel()
	got := map[string]string{}
	for _, s := range monoCovers([]app.Stack{
		// Cygnus Loop: H-a shows the Veil; Blue barely does.
		{Object: objectVeil, Filter: filterBlue, EffectiveSeconds: 7200},
		{Object: objectVeil, Filter: filterHa, EffectiveSeconds: 3600},
		// A single H-a sub doesn't beat a deep Red master.
		{Object: objectM31, Filter: "Red", EffectiveSeconds: 36000},
		{Object: objectM31, Filter: filterHa, EffectiveSeconds: 600},
		{Object: "M33", Filter: filterBlue, EffectiveSeconds: 100},
	}) {
		got[s.Object] = s.Filter
	}
	want := map[string]string{objectVeil: filterHa, objectM31: "Red", "M33": filterBlue}
	for o, f := range want {
		if got[o] != f {
			t.Errorf("%s: got %s, want %s", o, got[o], f)
		}
	}
}
