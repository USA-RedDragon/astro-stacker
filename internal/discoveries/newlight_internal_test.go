package discoveries

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestNewLightOfTheM31Nova(t *testing.T) {
	t.Parallel()
	night, ref, rate := 18.0562340917797, 18.49187715397442, 0.4
	nova := app.DiscoveryCandidate{ID: 18554, Kind: app.DiscoveryKindChange, Class: ClassNova, Sign: 1, NightMag: &night, RefMag: &ref}
	if m := NewLight(nova); m == nil || math.Abs(*m-19.26) > 0.01 {
		t.Errorf("the nova's new light %v, want 19.26: 18.06 is the spot's total with M 31's bulge", m)
	}
	alone := nova
	alone.RefMag = nil
	if m := NewLight(alone); m == nil || *m != night {
		t.Errorf("with nothing there before all the light is new: %v", m)
	}
	fade := nova
	fade.Sign = -1
	fade.NightMag, fade.RefMag = &ref, &night
	if m := NewLight(fade); m != nil {
		t.Errorf("a fading source added no light: %v", *m)
	}
	mover := app.DiscoveryCandidate{Kind: app.DiscoveryKindMover, Sign: 1, NightMag: &night, RateArcmin: &rate}
	if m := NewLight(mover); m == nil || *m != night {
		t.Errorf("a mover's magnitude is its own: %v", m)
	}
}
