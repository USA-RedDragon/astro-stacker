package mosaicplan

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
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

func TestNightsLeftCountsNightsAboveTheThreshold(t *testing.T) {
	t.Parallel()
	site := mosaics.Site{Lat: 32, Lon: -97}
	pts := []mosaics.Point{{RA: 186, Dec: 13}}
	now := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	n := nightsLeft(now, site, pts, 20)
	if n == 0 || n > 200 {
		t.Fatalf("nights left %d", n)
	}
	first := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if h := mosaics.DarkHours(first.AddDate(0, 0, n-1), site, pts, 20); h < usableMonthly {
		t.Errorf("last counted night has %v dark hours", h)
	}
	if h := mosaics.DarkHours(first.AddDate(0, 0, n), site, pts, 20); h >= usableMonthly {
		t.Errorf("first uncounted night has %v dark hours", h)
	}
	if nightsLeft(time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC), site, pts, 20) != 0 {
		t.Error("an autumn night counted for a spring target")
	}
}
