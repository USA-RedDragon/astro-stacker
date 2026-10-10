package mosaicplan

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestPlateauKeepsItsProgress(t *testing.T) {
	t.Parallel()
	gp := goals.Progress{Kind: goals.KindSNR, Goal: 10, Achieved: 6, SNR: 6, Progress: 0.36, Plateau: true, Done: true, HoursNeeded: 20, EffectiveHours: 3}
	pf, ok := panelFilter("Red", app.Stack{Object: "P1", EffectiveSeconds: 3 * 3600}, true, nil, gp, true)
	if !ok || pf.Progress != 0.36 || !pf.Done || pf.DoneReason != DonePlateau {
		t.Fatalf("plateau %+v", pf)
	}
	reached, _ := panelFilter("Red", app.Stack{Object: "P1"}, true, nil, goals.Progress{Progress: 1.2, Done: true}, true)
	if reached.Progress != 1.2 || reached.DoneReason != DoneGoal {
		t.Errorf("goal reached %+v", reached)
	}
	p := Panel{Filters: []PanelFilter{pf, {Filter: "Green", Progress: 0.5}}}
	if got := schedulingProgress(p); got != 0.5 {
		t.Errorf("scheduling progress %v", got)
	}
}
