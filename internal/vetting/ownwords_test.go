package vetting

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// TestOwnWords2217 replays source 2217's star:19 verdict: its Luminance
// night of 2026-10-12 covers the 0.45 mag deep dip, which the user's
// frames time 8.7 min late, but the why text said the night caught the
// shallower dip, as the timing's dip number counts from 1, and that the
// frames show no clear change, though only the night's average missed
// the change check. The text must name the deeper dip and the timed
// minimum.
func TestOwnWords2217(t *testing.T) {
	t.Parallel()
	e := &period.Ephemeris{Period: 4.28962998791928, T0: 2459066.0966055263, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.01664885060184273, Shape: 1.7652433525723104, Depth: map[string]float64{ztfR: 0.4540890742305946}},
			{Phase: 0.4988616839281336, Width: 0.013073283420880037, Shape: 0.3, Depth: map[string]float64{ztfR: 0.05512865632592344}},
		}}
	hours := [][2]float64{
		{-1.1164, 0.3075}, {-1.0273, 0.1298}, {-0.9382, 0.4885}, {-0.849, 0.2811}, {-0.7642, 0.3393}, {-0.6794, 0.4312},
		{-0.5898, 0.4158}, {-0.5013, 0.46}, {-0.4165, 0.6995}, {-0.3258, 0.5043}, {-0.241, 0.5771}, {-0.1514, 0.7678},
		{-0.0665, 0.5427}, {0.0237, 0.9791}, {0.1086, 0.842}, {0.1983, 0.2828}, {0.2831, 0.821}, {0.3732, 0.7593},
	}
	tm := e.T0 + 527*e.Period
	var pts []lc.Point
	curve := &ownimage.Curve{}
	for _, h := range hours {
		pts = append(pts, lc.Point{BJD: tm + h[0]/24, Mag: h[1], Err: 0.1918})
		curve.Points = append(curve.Points, ownimage.Point{BJD: tm + h[0]/24, Night: "2026-10-12", Filter: "Luminance", DMag: h[1], Err: 0.1918})
	}
	timing, ok := period.TimeMinimum(e, pts, ztfR)
	if !ok {
		t.Fatal("night 2026-10-12 not timed")
	}
	t.Logf("offset %.1f +- %.1f min, dip %d", timing.Offset, timing.OffsetErr, timing.Dip)
	if timing.Dip != 1 {
		t.Fatalf("timed dip %d, want 1, the deeper", timing.Dip)
	}
	if got := dipCaught(e, discoverysource.Summary{}, &timing, curve); got != "Your night caught the deeper of its two dips." {
		t.Errorf("dip caught: %q", got)
	}

	groups := []Group{{ID: check.GroupOwn, Checks: []check.Check{
		check.New(check.OwnChange, check.Fail, ""), timingCheck(e, &timing),
	}}}
	why := ownDoubt(ownimage.Flags{}, groups, period.KindEclipsing)
	if strings.Contains(why, "no clear change") || !strings.Contains(why, "time the dip at its predicted minimum") {
		t.Errorf("own doubt with a timed minimum: %q", why)
	}
	groups[0].Checks[1] = check.Skipped(check.OwnTiming, "")
	if why := ownDoubt(ownimage.Flags{}, groups, period.KindEclipsing); !strings.Contains(why, "no clear change") {
		t.Errorf("own doubt without a timed minimum: %q", why)
	}
}
