package vetting

import (
	"math"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

func starWith(g float64, nbs ...surveys.Neighbour) star {
	return star{found: true, target: surveys.Target{G: g}, neighbours: nbs}
}

func plainNotSure() Suggestion {
	return Suggestion{State: app.VetUnsure, Say: sayNotSure, Confidence: Low, Why: "The checks found nothing against it, and nothing that confirms it."}
}

func TestBlendCloseNeighbour(t *testing.T) {
	t.Parallel()
	st := starWith(15.50, surveys.Neighbour{G: 16.35, Sep: 1.6}, surveys.Neighbour{G: 20.73, Sep: 4.6}, surveys.Neighbour{G: 19.32, Sep: 7.1})
	s := blendOf(st, change{Mag: 0.104, Fainter: true}, math.NaN()).notSure(plainNotSure())
	if s.State != app.VetUnsure || s.Say != "Not sure: a nearby star blends with it" || !strings.Contains(s.Why, "G 16.35 1.6\" away") {
		t.Errorf("299: %+v", s)
	}
}

func TestBlendCrowdedCore(t *testing.T) {
	t.Parallel()
	st := starWith(18.52,
		surveys.Neighbour{G: 19.18, Sep: 2.55}, surveys.Neighbour{G: 20.48, Sep: 3.24}, surveys.Neighbour{G: 19.74, Sep: 3.39},
		surveys.Neighbour{G: 19.98, Sep: 3.43}, surveys.Neighbour{G: 19.64, Sep: 3.55})
	b := blendOf(st, change{Mag: 0.973, Fainter: true}, 17.21)
	if b.over < 1.9 || b.over > 2.1 {
		t.Errorf("45467 over %.2f, want about 2.0", b.over)
	}
	s := b.notSure(plainNotSure())
	if s.State != app.VetUnsure || s.Say != "Not sure: the change is more light than the star has" {
		t.Errorf("45467: %+v", s)
	}
	crowd := starWith(18.52, surveys.Neighbour{G: 19.18, Sep: 2.55}, surveys.Neighbour{G: 19.04, Sep: 2.9}, surveys.Neighbour{G: 19.64, Sep: 3.55})
	s = blendOf(crowd, change{Mag: 0.1, Fainter: true}, math.NaN()).notSure(plainNotSure())
	if s.Say != "Not sure: too crowded to tell which star changed" || !strings.Contains(s.Why, "2 Gaia stars") {
		t.Errorf("crowded core: %+v", s)
	}
}

func TestBlendLeavesOthers(t *testing.T) {
	t.Parallel()
	st := starWith(15.19, surveys.Neighbour{G: 16.03, Sep: 2.3})
	want := Suggestion{State: app.VetReal, Say: "Looks like a new eclipsing binary, repeats every 49.642 days"}
	if s := blendOf(st, change{Mag: 0.202, Fainter: true}, 14.88).notSure(want); s != want {
		t.Errorf("1592 changed: %+v", s)
	}
	pending := Suggestion{State: app.VetUnsure, Say: "Not sure: no survey saw it change"}
	if s := blendOf(st, change{Mag: 0.202, Fainter: true}, 14.88).notSure(pending); s != pending {
		t.Errorf("other Not sure changed: %+v", s)
	}
	lone := starWith(16.89, surveys.Neighbour{G: 18.72, Sep: 1.9}, surveys.Neighbour{G: 19.48, Sep: 2.5})
	if s := blendOf(lone, change{Mag: 0.3, Fainter: true}, math.NaN()).notSure(plainNotSure()); s.Say != sayNotSure {
		t.Errorf("15523 faint neighbours: %+v", s)
	}
	deep := starWith(15.0, surveys.Neighbour{G: 15.8, Sep: 2.0})
	if s := blendOf(deep, change{Mag: 1.2, Fainter: true}, math.NaN()).notSure(plainNotSure()); s.Say != sayNotSure {
		t.Errorf("fade deeper than the neighbour's light: %+v", s)
	}
}
