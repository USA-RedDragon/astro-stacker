package server

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestMonoCoversPreferNebulaFilters(t *testing.T) {
	got := map[string]string{}
	for _, s := range monoCovers([]app.Stack{
		// Cygnus Loop: H-a shows the Veil; Blue barely does.
		{Object: "Veil", Filter: "Blue", EffectiveSeconds: 7200},
		{Object: "Veil", Filter: "H-a", EffectiveSeconds: 3600},
		// A single H-a sub doesn't beat a deep Red master.
		{Object: "M31", Filter: "Red", EffectiveSeconds: 36000},
		{Object: "M31", Filter: "H-a", EffectiveSeconds: 600},
		{Object: "M33", Filter: "Blue", EffectiveSeconds: 100},
	}) {
		got[s.Object] = s.Filter
	}
	want := map[string]string{"Veil": "H-a", "M31": "Red", "M33": "Blue"}
	for o, f := range want {
		if got[o] != f {
			t.Errorf("%s: got %s, want %s", o, got[o], f)
		}
	}
}
