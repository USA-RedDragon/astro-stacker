package vetting

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

func readZstdJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text, err := unzstd(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(text, v); err != nil {
		t.Fatal(err)
	}
}

// eph76400 is the stored vet's ephemeris for source 76400.
func eph76400() *period.Ephemeris {
	return &period.Ephemeris{Period: 4.111826139758551, T0: 2459594.91716415, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.02452728634065693, Shape: 1.9512372880825228, Depth: map[string]float64{ztfG: 0.16216042743382236, ztfR: 0.16094644278440656}},
			{Phase: 0.49597568533570263, Width: 0.030438746777377738, Shape: 3.1562606247924645, Depth: map[string]float64{ztfG: 0.14403678055299074, ztfR: 0.1415178152656785}},
		}}
}

// TestOwnTiming76400 replays source 76400 (Gaia DR3 2013040936747919488,
// Garlic Nebula), a 4.112 day eclipsing binary flagged on 2026-10-07,
// whose frames that night caught only an egress. The vet timed the
// flagged night only and said the frames covered no predicted dip, but
// the field's O-III frames of 2026-02-19 sit at -0.61, -0.27, +0.07 and
// +0.42 h from a predicted primary and are 0.107, 0.146, 0.133 and 0.100
// mag faint. The fixture is the audit's aperture photometry of the 49
// subs of 2026-02-18 to 2026-02-21 and 2026-10-07, each filter set to its
// median out of eclipse, with its out of eclipse scatter as the error.
// The check must time the 2026-02-19 primary about 0.1 to 0.15 mag deep
// and on time, and a flagged night that times a dip itself must still
// be the one reported.
func TestOwnTiming76400(t *testing.T) {
	t.Parallel()
	eph76400 := eph76400()
	var c ownimage.Curve
	readZstdJSON(t, "testdata/own76400_curve.json.zst", &c)
	var sum discoverysource.Summary
	sum.Best.Night = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	all := ownTimings(eph76400, ownSets(&c))
	for _, tm := range all {
		t.Logf("%s %s: dip %d, %.1f +- %.1f min, %.3f mag, %d frames", tm.Night, tm.Filter, tm.Dip, tm.Offset, tm.OffsetErr, tm.Depth, tm.Points)
	}
	tm := ownTimingOf(&period.Result{Ephemeris: eph76400}, &c, sum)
	if tm == nil {
		t.Fatal("no night timed")
	}
	if tm.Night != "2026-02-19" || tm.Dip != 1 {
		t.Fatalf("timed %s dip %d, want the 2026-02-19 primary", tm.Night, tm.Dip)
	}
	if math.Abs(tm.Offset) > 3*tm.OffsetErr {
		t.Errorf("minimum %.1f +- %.1f min from the prediction", tm.Offset, tm.OffsetErr)
	}
	if tm.Depth < 0.10 || tm.Depth > 0.15 {
		t.Errorf("depth %.3f mag, want 0.10 to 0.15", tm.Depth)
	}
	if got := dipCaught(eph76400, sum, tm, &c); got != "Your frames of Feb 19 caught the deeper of its two dips." {
		t.Errorf("dip caught: %q", got)
	}
	if nc := nightChart(sum, &c, eph76400, tm); nc.Title != "Your night, Feb 19" || len(nc.Points) != 5 {
		t.Errorf("night chart %q with %d frames, want the 5 O-III frames of Feb 19", nc.Title, len(nc.Points))
	}
	ch := timingCheck(eph76400, tm)
	if ch.Status != check.Pass || !strings.Contains(ch.Evidence, "Feb 19") {
		t.Errorf("own timing %s: %q", ch.Status, ch.Evidence)
	}
	sum.Best.Night = time.Date(2026, 2, 21, 0, 0, 0, 0, time.UTC)
	if f := ownTimingOf(&period.Result{Ephemeris: eph76400}, &c, sum); f == nil || f.Night != "2026-02-21" || f.Dip != 2 {
		t.Errorf("flagged 2026-02-21 timed as %+v, want its own secondary", f)
	}
}
