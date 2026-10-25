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
	filterGreen = "Green"
	filterHa    = "H-a"
	filterRed   = "Red"
	projectRho  = "Rho"
)

func TestMonoCoversPreferNebulaFilters(t *testing.T) {
	t.Parallel()
	got := map[string]string{}
	for _, s := range monoCovers([]app.Stack{
		// Cygnus Loop: H-a shows the Veil; Blue barely does.
		{Object: objectVeil, Filter: filterBlue, EffectiveSeconds: 7200},
		{Object: objectVeil, Filter: filterHa, EffectiveSeconds: 3600},
		// A single H-a sub doesn't beat a deep Red master.
		{Object: objectM31, Filter: filterRed, EffectiveSeconds: 36000},
		{Object: objectM31, Filter: filterHa, EffectiveSeconds: 600},
		{Object: "M33", Filter: filterBlue, EffectiveSeconds: 100},
	}) {
		got[s.Object] = s.Filter
	}
	want := map[string]string{objectVeil: filterHa, objectM31: filterRed, "M33": filterBlue}
	for o, f := range want {
		if got[o] != f {
			t.Errorf("%s: got %s, want %s", o, got[o], f)
		}
	}
}

// Masters as stored on 2026-10-24. Sculptor Galaxy's red, green and blue
// subs scored 0.03 to 0.05 under a 90% Moon; its H-a scored 0.61. Heart and
// Soul Nebula Panel 1's broadband scored under 0.01; its one S-II sub, at
// 0.03, is narrowband and still shows the field.
func TestMonoCoversSkipPoorScores(t *testing.T) {
	t.Parallel()
	const sculptor, heart = "Sculptor Galaxy", "Heart and Soul Nebula Panel 1"
	got := map[string]string{}
	for _, s := range monoCovers([]app.Stack{
		{Object: sculptor, Filter: filterRed, EffectiveSeconds: 49.183679186718514, ExposureSeconds: 1200},
		{Object: sculptor, Filter: filterGreen, EffectiveSeconds: 36.91992301516527, ExposureSeconds: 900},
		{Object: sculptor, Filter: filterBlue, EffectiveSeconds: 38.61770420550303, ExposureSeconds: 1200},
		{Object: sculptor, Filter: filterHa, EffectiveSeconds: 367.57951664299975, ExposureSeconds: 600},
		{Object: sculptor, Filter: filterLuminance, EffectiveSeconds: 1169.5408129126656, ExposureSeconds: 1800},
		{Object: heart, Filter: filterRed, EffectiveSeconds: 2.704236222461313, ExposureSeconds: 300},
		{Object: heart, Filter: filterBlue, EffectiveSeconds: 1.2900559651188337, ExposureSeconds: 300},
		{Object: heart, Filter: filterSII, EffectiveSeconds: 17.18353930702075, ExposureSeconds: 600},
		{Object: "Broadband only", Filter: filterRed, EffectiveSeconds: 2.704236222461313, ExposureSeconds: 300},
	}) {
		got[s.Object] = s.Filter
	}
	if f, ok := got["Broadband only"]; ok {
		t.Errorf("broadband only: got a %s cover from subs scoring 0.009", f)
	}
	if got[sculptor] != filterHa {
		t.Errorf("%s: got %q, want H-a", sculptor, got[sculptor])
	}
	if got[heart] != filterSII {
		t.Errorf("%s: got %q, want S-II", heart, got[heart])
	}
}

func TestFewestPanelsPerProject(t *testing.T) {
	t.Parallel()
	got := fewestPanels([]app.Mosaic{
		{Project: "Veil", Filter: filterHa, Panels: 4, PanelsTotal: 4},
		{Project: "Veil", Filter: filterBlue, Panels: 3, PanelsTotal: 4},
		{Project: objectOrion, Filter: filterHa, Panels: 2, PanelsTotal: 2},
	}, nil)
	if v := got["Veil"]; v.Filter != filterBlue || v.Panels != 3 || v.PanelsTotal != 4 {
		t.Fatalf("veil %+v", v)
	}
	if o := got[objectOrion]; o.Panels != 2 {
		t.Fatalf("orion %+v", o)
	}
}

// Rho's RGB cover holds five panels; its two-panel H-a mosaic isn't in it.
func TestFewestPanelsOnlyInTheCoverPalette(t *testing.T) {
	t.Parallel()
	mosaics := []app.Mosaic{
		{Project: projectRho, Filter: filterRed, Panels: 5, PanelsTotal: 12},
		{Project: projectRho, Filter: "Green", Panels: 5, PanelsTotal: 12},
		{Project: projectRho, Filter: filterBlue, Panels: 5, PanelsTotal: 12},
		{Project: projectRho, Filter: filterHa, Panels: 2, PanelsTotal: 12},
		{Project: projectRho, Filter: "Luminance", Panels: 1, PanelsTotal: 12},
	}
	for palette, want := range map[string]int{"RGB": 5, "RGB+Ha": 2, "RGB+OIII": 5} {
		if got := fewestPanels(mosaics, map[string]string{projectRho: palette})[projectRho]; got.Panels != want {
			t.Errorf("%s: %d panels, want %d", palette, got.Panels, want)
		}
	}
}
