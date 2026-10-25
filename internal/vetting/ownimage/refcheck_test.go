package ownimage

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const m22Night = "2021-06-29"

func refPlan(fade bool, aps map[int]float64, sets []refSet) (*spotPlan, *ensemble) {
	night := time.Date(2021, 6, 29, 0, 0, 0, 0, time.UTC)
	sign := 1
	if fade {
		sign = -1
	}
	sp := &spotPlan{spot: Spot{Nights: []Night{{Night: night, Group: discoveries.GroupBroadband, Sign: sign}}}, sets: sets}
	e := &ensemble{transp: map[int]float64{}}
	n := 0
	for i := range aps {
		n = max(n, i+1)
	}
	sp.recs = make([]*rec, n)
	for i, ap := range aps {
		sp.recs[i] = &rec{ap: ap, peak: ap / 5, noise: 60, sigma: 1.6, apOK: true}
		e.transp[i] = 1
	}
	return sp, e
}

// TestTwoSubReferenceFindsTheCosmicRay is candidate 230822 (M 22, Blue,
// 2021-06-29): the Blue reference held two subs, and the one of 2021-05-23
// 09:09 had five times the other's light on the spot from a cosmic ray.
// The check skipped the Blue set for having under three subs and passed on
// Green alone; it must name the 09:09 sub.
func TestTwoSubReferenceFindsTheCosmicRay(t *testing.T) {
	t.Parallel()
	blue := refSet{night: m22Night, filter: synthBlue, refs: []int{0, 1}, nights: []int{2, 3, 4, 5}}
	sp, e := refPlan(true, map[int]float64{0: 1089, 1: 5382, 2: 1021, 3: 955, 4: 977, 5: 892}, []refSet{blue})
	ev := evalRef(context.Background(), sp, e, blue)
	if !ev.ok || len(ev.outliers) != 1 || ev.outliers[0] != 1 {
		t.Fatalf("outliers %v (measured %v)", ev.outliers, ev.ok)
	}
	if ev.signif < refSignif {
		t.Errorf("the cosmic ray stands %.1f sigma out", ev.signif)
	}
	sp, e = refPlan(true, map[int]float64{0: 1089, 1: 1130, 2: 1021, 3: 955}, nil)
	if ev := evalRef(context.Background(), sp, e, refSet{night: m22Night, filter: synthBlue, refs: []int{0, 1}, nights: []int{2, 3}}); !ev.ok || len(ev.outliers) != 0 {
		t.Errorf("two clean subs: outliers %v (measured %v)", ev.outliers, ev.ok)
	}
}

// TestRefCleanNeverPassesOnASkippedFilter: a Green set of three clean subs
// beside a Blue set with one usable reference sub must not pass.
func TestRefCleanNeverPassesOnASkippedFilter(t *testing.T) {
	t.Parallel()
	green := refSet{night: m22Night, filter: "Green", refs: []int{0, 1, 2}, nights: []int{3}}
	blue := refSet{night: m22Night, filter: synthBlue, refs: []int{4}, nights: []int{5}}
	sp, e := refPlan(true, map[int]float64{0: 1000, 1: 1010, 2: 990, 3: 1000, 4: 5000, 5: 1000}, []refSet{green, blue})
	subs := make([]Sub, 6)
	got, _ := refClean(context.Background(), subs, sp, e, true)
	if got.Status == check.Pass {
		t.Fatalf("passed with Blue not measured: %s", got.Evidence)
	}
	t.Log(got.Evidence)
}
