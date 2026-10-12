package skyevents_test

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
)

func TestShortSecondsFollowsFluxAndArea(t *testing.T) {
	t.Parallel()
	if got := skyevents.ShortSeconds(0, 0, 21.5, 1.91, 4); math.Abs(got-0.0011) > 1e-9 {
		t.Errorf("mag 0 point source: %v s, want 0.0011 (32768 ADU × 19.88 px² / 5.75e8 ADU/s)", got)
	}
	if a, b := skyevents.ShortSeconds(1, 0, 21.5, 1.91, 4), skyevents.ShortSeconds(-1, 0, 21.5, 1.91, 4); a <= b {
		t.Errorf("a fainter body gets a shorter sub: %v <= %v", a, b)
	}
	if a, b := skyevents.ShortSeconds(-2.5, 45.0/3600, 21.5, 1.91, 4), skyevents.ShortSeconds(-2.5, 0, 21.5, 1.91, 4); a <= b {
		t.Errorf("a disc spreads the light: %v <= %v", a, b)
	}
	if got := skyevents.ShortSeconds(-12.7, 0.52, 21.5, 1.91, 4); got != 0.00035 {
		t.Errorf("full Moon: %v, want 0.00035", got)
	}
	if got := skyevents.ShortSeconds(-4.4, 15.0/3600, 21.5, 1.91, 4); got != skyevents.MinShortSeconds {
		t.Errorf("Venus is floored at the camera's practical minimum: %v", got)
	}
}

func TestEstimateExposureSaysWhatIsMissing(t *testing.T) {
	t.Parallel()
	mag := 0.9
	e := skyevents.Event{Kind: skyevents.EventBodyDSO, Members: []skyevents.Member{{Name: "Mars", Kind: skyevents.KindPlanet, Magnitude: &mag, Diameter: 0.0015}, {Name: "M 44", Kind: skyevents.KindDSO}}}
	x := skyevents.EstimateExposure(e, skyevents.Photometry{Filter: "L"})
	if x.ShortSeconds != nil || x.NormalSeconds != nil || !strings.Contains(x.Basis, "not measured yet") {
		t.Fatalf("%+v", x)
	}
	zp, scale, hfr, normal := 21.5, 1.91, 4.0, 180.0
	x = skyevents.EstimateExposure(e, skyevents.Photometry{Filter: "L", ZeroPoint: &zp, ZPSamples: 3, Scale: &scale, HFR: &hfr, Normal: &normal, NormalSrc: "test"})
	if x.ShortSeconds == nil || *x.ShortSeconds >= 1 || x.NormalSeconds == nil || *x.NormalSeconds != 180 || !strings.Contains(x.Basis, "Mars (magnitude 0.9)") {
		t.Fatalf("%+v", x)
	}
	moon := -10.0
	withMoon := skyevents.Event{Kind: skyevents.EventBodyBody, Members: []skyevents.Member{{Name: "Moon", Kind: skyevents.KindMoon, Magnitude: &moon, Diameter: 0.52}, e.Members[0]}}
	x = skyevents.EstimateExposure(withMoon, skyevents.Photometry{Filter: "L", ZeroPoint: &zp, Scale: &scale, HFR: &hfr})
	if mars := skyevents.ShortSeconds(mag, 0.0015, zp, scale, hfr); x.ShortSeconds == nil || *x.ShortSeconds != mars || !strings.Contains(x.Basis, "of Mars") {
		t.Errorf("the Moon's disc spreads its light, so Mars sets the short sub (%v): %+v", mars, x)
	}
	e.Kind = skyevents.EventBodyBody
	if x = skyevents.EstimateExposure(e, skyevents.Photometry{Filter: "L", ZeroPoint: &zp, Scale: &scale, HFR: &hfr, Normal: &normal}); x.NormalSeconds != nil {
		t.Errorf("two bodies get no normal set: %+v", x)
	}
}
