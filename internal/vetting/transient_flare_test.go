package vetting

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
)

const flareNight = "2026-05-13"

// flareSummary is 52576 on Markarian Chain Panel 8: found at 9.5 over 36
// subs with no magnitude.
func flareSummary() discoverysource.Summary {
	night := nightDate(flareNight)
	return discoverysource.Summary{Members: []app.DiscoveryCandidate{
		{Sign: 1, SNR: 9.53, SubsTested: 36, Night: night, Group: discoveries.GroupBroadband},
	}}
}

// flareCurve is 52576's own light: 3.2 over 41 frames in all filters.
func flareCurve() *ownimage.FluxCurve {
	n := zNight(flareNight, ownimage.AllFilters, 3.2, 41, true)
	n.Empty99 = 25
	return curveOf(n)
}

func TestReportedLightWithoutMagnitudeKeepsItsSNR(t *testing.T) {
	t.Parallel()
	reps := reportedLights(flareSummary())
	r := reportedFor(reps, ownimage.AllFilters, flareNight)
	if r.ok || r.found != 9.53 || r.subs != 36 {
		t.Fatalf("52576's light %+v", r)
	}
	if b := reportedBroad(reps); b.ok {
		t.Errorf("light with no magnitude is not the brightest: %+v", b)
	}
	m := 19.0
	sum := flareSummary()
	sum.Members = append(sum.Members, app.DiscoveryCandidate{Sign: 1, SNR: 6, SubsTested: 36, Night: nightDate(flareNight), Group: discoveries.GroupBroadband, NightMag: &m})
	if r := reportedFor(reportedLights(sum), lum, flareNight); !r.ok || r.mag != 19 || r.bySNR() {
		t.Errorf("a measured magnitude wins: %+v", r)
	}
}

func TestStackRunsOnLightReportedBySNR(t *testing.T) {
	t.Parallel()
	c, v := transientStack(flareCurve(), reportedLights(flareSummary()))
	if v == ownimage.StackUntested || c.Status != check.Fail {
		t.Fatalf("52576: %s %s %s", v, c.Status, c.Evidence)
	}
	want := 9.53 * math.Sqrt(41.0/36)
	if !strings.Contains(c.Evidence, fmt.Sprintf("shows at %.1f in the same frames", want)) || !strings.Contains(c.Evidence, "put no magnitude on it") {
		t.Errorf("52576 evidence: %s", c.Evidence)
	}
	sum := flareSummary()
	sum.Members[0].SNR = 30
	n := zNight(flareNight, ownimage.AllFilters, 32, 41, true)
	n.Empty99 = 25
	if c, v := transientStack(curveOf(n), reportedLights(sum)); v != ownimage.StackConfirmed {
		t.Errorf("light found at 30 that shows at 32: %s %s", v, c.Evidence)
	}
}

// flareATLAS is ATLAS's nights around 52576: flat, with errors of e
// microjansky on the flagged night and each day around it.
func flareATLAS(e float64) []surveys.ATLASRow {
	rows := make([]surveys.ATLASRow, 0, 120)
	start := mjdOf(time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC))
	for d := range 120 {
		rows = append(rows, surveys.ATLASRow{MJD: start + float64(d) + 0.3, UJy: 0.5 * math.Sin(float64(d)), DUJy: e, Filter: "o", Obs: "01a60123o0123o"})
	}
	return rows
}

func TestATLASMissedTheReportedLight(t *testing.T) {
	t.Parallel()
	sum := flareSummary()
	fx := transientFacts{flux: flareCurve(), flagged: []time.Time{nightDate(flareNight)}, reps: reportedLights(sum), atlas: surveys.Fetch{State: surveys.Done}, rows: flareATLAS(3)}
	c := atlasCheckTransient(fx)
	if c.Status != check.Fail || !strings.Contains(c.Evidence, "saw nothing") || !strings.Contains(c.Evidence, "put no magnitude") {
		t.Fatalf("deep ATLAS frames: %s %s", c.Status, c.Evidence)
	}
	if s := transientSuggest([]Group{{ID: check.GroupTransient, Checks: []check.Check{c}}}, fx); s.State != app.VetArtifact {
		t.Errorf("suggestion %+v", s)
	}
	shallow := fx
	shallow.rows = flareATLAS(20)
	if c := atlasCheckTransient(shallow); c.Status != check.NotRun {
		t.Errorf("shallow ATLAS frames: %s %s", c.Status, c.Evidence)
	}
	star := fx
	star.near = []counterpart{{g: 14, sep: 4}}
	if c := atlasCheckTransient(star); c.Status != check.NotRun {
		t.Errorf("bright neighbour: %s %s", c.Status, c.Evidence)
	}
	galaxy := fx
	galaxy.matches = []discoveries.Match{{Catalog: catalogGalaxy, Name: "NGC 4438"}}
	if c := atlasCheckTransient(galaxy); c.Status != check.NotRun {
		t.Errorf("in a galaxy: %s %s", c.Status, c.Evidence)
	}
	seen := fx
	seen.rows = flareATLAS(3)
	for i := range seen.rows {
		if math.Abs(seen.rows[i].MJD-mjdOf(nightDate(flareNight).Add(nightMid))) < 1 {
			seen.rows[i].UJy = 40
		}
	}
	if c := atlasCheckTransient(seen); c.Status == check.Fail {
		t.Errorf("ATLAS saw it: %s %s", c.Status, c.Evidence)
	}
}
