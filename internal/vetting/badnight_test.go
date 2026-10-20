package vetting

import (
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

func flagged(run int, night, group string, sign int) app.DiscoveryCandidate {
	n, _ := time.Parse(time.DateOnly, night)
	return app.DiscoveryCandidate{RunID: run, Night: n, Group: group, Kind: app.DiscoveryKindChange, Sign: sign}
}

func noSurveySaw() Suggestion {
	return Suggestion{State: app.VetUnsure, Say: sayNoSurveySaw, Confidence: Low,
		Why: "Flat: 1,180 r points from 2018 to 2025, steady at 15.13 with 0.01 mag scatter. Your own frames did not measure the change, so only the detection says it changed."}
}

func ztfGroups(ev string) []Group {
	return []Group{{Checks: []check.Check{{ID: check.ZTF, Status: check.Fail, Evidence: ev}}}}
}

// TestBadNights is failure mode N1: 28575, 28454 (Abell 85, 2026-01-17)
// and 30023 (Cygnus Loop Panel 2, 2026-08-12) were left Not sure, though
// every night they were flagged on had nearly all its changes going their
// way, and the surveys saw the stars steady for years.
func TestBadNights(t *testing.T) {
	t.Parallel()
	abell := map[int]runSkew{394: {100, 91}, 396: {96, 85}, 398: {283, 159}}
	abellRuns := map[int]app.DiscoveryRun{396: {HFRNight: 1.9045, HFRRef: 1.6198}}
	why := badNights([]app.DiscoveryCandidate{flagged(396, "2026-01-17", "O-III", -1)}, abell, abellRuns)
	if !strings.Contains(why, "On Jan 17, 85 of the 96 changes found in O-III were fades, with stars 1.18 times as wide") {
		t.Errorf("28575: %q", why)
	}
	s := onBadNights(noSurveySaw(), why, ztfGroups("Flat: 1,180 r points."))
	if s.State != app.VetArtifact || s.Say != sayBadNight || s.Confidence != Medium || !strings.Contains(s.Why, "Flat: 1,180 r points.") {
		t.Errorf("28575 verdict: %+v", s)
	}
	m28454 := []app.DiscoveryCandidate{flagged(394, "2026-01-17", "H-a", -1), flagged(396, "2026-01-17", "O-III", -1), flagged(398, "2026-01-17", "broadband", -1)}
	if why := badNights(m28454, abell, nil); !strings.Contains(why, "91 of the 100 changes found in H-a") {
		t.Errorf("28454: %q", why)
	}
	cyg := badNights([]app.DiscoveryCandidate{flagged(444, "2026-08-12", "H-a", -1)}, map[int]runSkew{444: {264, 235}}, nil)
	if s := onBadNights(plainNotSure(), cyg, nil); s.State != app.VetArtifact || s.Confidence != Low {
		t.Errorf("30023 as plain Not sure: %+v", s)
	}
	for _, c := range []struct {
		name    string
		members []app.DiscoveryCandidate
		skews   map[int]runSkew
	}{
		{"236 on a run of 69% fades", []app.DiscoveryCandidate{flagged(9, "2026-10-11", "broadband", -1)}, map[int]runSkew{9: {84, 58}}},
		{"16727 on a run of 78% brightenings", []app.DiscoveryCandidate{flagged(710, "2025-06-27", "broadband", 1)}, map[int]runSkew{710: {182, 40}}},
		{"a good night beside a bad one", []app.DiscoveryCandidate{flagged(396, "2026-01-17", "O-III", -1), flagged(9, "2026-10-11", "broadband", -1)},
			map[int]runSkew{396: {96, 85}, 9: {84, 58}}},
		{"a brightening on a night of fades", []app.DiscoveryCandidate{flagged(396, "2026-01-17", "O-III", 1)}, abell},
		{"too few changes to judge", []app.DiscoveryCandidate{flagged(1, "2026-01-17", "O-III", -1)}, map[int]runSkew{1: {40, 39}}},
		{"no counts", []app.DiscoveryCandidate{flagged(396, "2026-01-17", "O-III", -1)}, nil},
	} {
		if why := badNights(c.members, c.skews, nil); why != "" {
			t.Errorf("%s: %q", c.name, why)
		}
	}
	for _, keep := range []Suggestion{
		{State: app.VetReal, Say: "Looks like a new variable star, fades now and then"},
		{State: app.VetUnsure, Say: "Not sure: periodic, but the cycle drifts"},
		{State: app.VetUnsure, Say: sayVariesUnconfirmed},
	} {
		if s := onBadNights(keep, why, nil); s != keep {
			t.Errorf("%s changed: %+v", keep.Say, s)
		}
	}
}
