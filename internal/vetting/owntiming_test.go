package vetting

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
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
	all := ownTimings(eph76400, ownSets(&c, sum))
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

type diffFixture struct {
	Candidates []struct {
		SourceID int    `json:"source_id"`
		ID       int    `json:"id"`
		Night    string `json:"night"`
		Light    string `json:"light"`
	} `json:"candidates"`
	Curve70878 []struct {
		FrameID int     `json:"frame_id"`
		DMag    float64 `json:"dmag"`
		Err     float64 `json:"err"`
	} `json:"curve_70878_o3"`
}

func diffSummary(t *testing.T, fx diffFixture, source int, ra, dec float64, flagged string) discoverysource.Summary {
	t.Helper()
	var sum discoverysource.Summary
	sum.Source.ID, sum.Source.RA, sum.Source.Dec = source, ra, dec
	for _, c := range fx.Candidates {
		if c.SourceID != source {
			continue
		}
		night, err := time.Parse(time.DateOnly, c.Night)
		if err != nil {
			t.Fatal(err)
		}
		m := app.DiscoveryCandidate{Night: night, Light: c.Light}
		m.ID = c.ID
		sum.Members = append(sum.Members, m)
		if c.Night == flagged {
			sum.Best = m
		}
	}
	return sum
}

// TestOwnTiming1161 replays source 1161 (Gaia DR3 1859873813031676160,
// Cygnus Loop), a 1.022 day eclipsing binary whose flagged night of
// 2026-09-16 is out of eclipse. The vet said its frames covered no dip,
// but the detection's difference image photometry of the S-II frames of
// 2025-11-24 covers a primary: an eclipse fit gives 0.44 +- 0.18 mag at
// +1 +- 14 min. The fixture is the stored candidates' difference image
// light. The check must time that primary within 15 min of the
// prediction.
func TestOwnTiming1161(t *testing.T) {
	t.Parallel()
	var fx diffFixture
	readZstdJSON(t, "testdata/own_diff_1161_70878.json.zst", &fx)
	e := &period.Ephemeris{Period: 1.0222308177247226, T0: 2459066.4298939696, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.07939561725461472, Shape: 8, Depth: map[string]float64{ztfG: 0.20672897919752747, ztfR: 0.1992019118813302}},
			{Phase: 0.500716415316066, Width: 0.04032011551865953, Shape: 2.0640701564969244, Depth: map[string]float64{ztfG: 0.07602965095511162, ztfR: 0.08584689332063786}},
		}}
	sum := diffSummary(t, fx, 1161, 312.1358734742287, 31.837173218341423, "2026-09-16")
	for _, tm := range ownTimings(e, ownSets(nil, sum)) {
		t.Logf("%s %s diff %v: dip %d, %.1f +- %.1f min, %.3f mag, %d frames", tm.Night, tm.Filter, tm.Diff, tm.Dip, tm.Offset, tm.OffsetErr, tm.Depth, tm.Points)
	}
	tm := ownTimingOf(&period.Result{Ephemeris: e}, nil, sum)
	if tm == nil {
		t.Fatal("no night timed")
	}
	if tm.Night != "2025-11-24" || tm.Filter != "S-II" || tm.Dip != 1 || !tm.Diff {
		t.Fatalf("timed %+v, want the S-II primary of 2025-11-24 from the difference images", *tm)
	}
	if math.Abs(tm.Offset) > 15 {
		t.Errorf("minimum %.1f min from the prediction, want within 15", tm.Offset)
	}
	if ch := timingCheck(e, tm); ch.Status != check.Pass {
		t.Errorf("own timing %s: %q", ch.Status, ch.Evidence)
	}
	if nc := nightChart(sum, nil, e, tm); nc.Title != "Your night, Nov 24" || len(nc.Points) != 5 {
		t.Errorf("night chart %q with %d frames (%s)", nc.Title, len(nc.Points), nc.Empty)
	}
}

// TestOwnTiming70878 replays source 70878 (Gaia DR3 1858173349582316032,
// Cygnus Loop), a 9.097 day eccentric eclipsing binary flagged on
// 2026-08-08 during a secondary eclipse. Its comparison star curve that
// night scatters 0.087 mag, from a 0.85 arcsec companion, neighbours and
// O-III nebulosity, and did not time the dip, so the vet said its frames
// covered none. The detection's difference image photometry of the same
// O-III frames shows the secondary: an eclipse fit gives 0.218 +- 0.055
// mag at +10 +- 19 min. The fixture holds the stored candidates'
// difference image light and the stored vet chart's comparison star
// values of the same eight frames. The check must report the secondary
// within 30 min, about 0.2 mag deep.
func TestOwnTiming70878(t *testing.T) {
	t.Parallel()
	var fx diffFixture
	readZstdJSON(t, "testdata/own_diff_1161_70878.json.zst", &fx)
	e := &period.Ephemeris{Period: 9.096889225114335, T0: 2459066.778630674, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.023147951815415888, Shape: 8, Depth: map[string]float64{ztfG: 0.4338942450314148, ztfR: 0.46322846781136606}},
			{Phase: 0.3009587870393177, Width: 0.021499421018785866, Shape: 5.442969455902474, Depth: map[string]float64{ztfG: 0.3605298490447281, ztfR: 0.3846286940178936}},
		}}
	sum := diffSummary(t, fx, 70878, 312.37552421425517, 29.05656400579651, "2026-08-08")
	bjd := map[int]float64{}
	for _, s := range ownSets(nil, sum) {
		if s.filter != "O-III" {
			continue
		}
		var light []struct {
			FrameID int       `json:"frame_id"`
			Time    time.Time `json:"time"`
		}
		for _, c := range fx.Candidates {
			if c.ID == 258258 {
				if err := json.Unmarshal([]byte(c.Light), &light); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, l := range light {
			bjd[l.FrameID] = lc.BJD(l.Time, sum.Source.RA, sum.Source.Dec)
		}
	}
	curve := &ownimage.Curve{Comps: 15, Scatter: 0.0867}
	for _, p := range fx.Curve70878 {
		curve.Points = append(curve.Points, ownimage.Point{FrameID: p.FrameID, BJD: bjd[p.FrameID], Night: "2026-08-08", Filter: "O-III", DMag: p.DMag, Err: p.Err})
	}
	if got := ownTimings(e, ownSets(curve, discoverysource.Summary{})); len(got) != 0 {
		t.Errorf("the comparison star curve alone timed %+v; the replay needs it confused", got)
	}
	for _, tm := range ownTimings(e, ownSets(curve, sum)) {
		t.Logf("%s %s diff %v: dip %d, %.1f +- %.1f min, %.3f mag, %d frames", tm.Night, tm.Filter, tm.Diff, tm.Dip, tm.Offset, tm.OffsetErr, tm.Depth, tm.Points)
	}
	tm := ownTimingOf(&period.Result{Ephemeris: e}, curve, sum)
	if tm == nil {
		t.Fatal("no night timed")
	}
	if tm.Night != "2026-08-08" || tm.Dip != 2 || !tm.Diff {
		t.Fatalf("timed %+v, want the 2026-08-08 secondary from the difference images", *tm)
	}
	if math.Abs(tm.Offset) > 30 {
		t.Errorf("minimum %.1f min from the prediction, want within 30", tm.Offset)
	}
	if tm.Depth < 0.14 || tm.Depth > 0.3 {
		t.Errorf("depth %.3f mag, want about 0.2", tm.Depth)
	}
	ch := timingCheck(e, tm)
	if ch.Status != check.Pass || !strings.Contains(ch.Evidence, "difference images of Aug 8") {
		t.Errorf("own timing %s: %q", ch.Status, ch.Evidence)
	}
	if got := dipCaught(e, sum, tm, curve); got != "Your night caught the shallower of its two dips." {
		t.Errorf("dip caught: %q", got)
	}
}
