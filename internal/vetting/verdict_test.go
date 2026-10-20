package vetting

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

func TestFaintSurveyCycleNotReal(t *testing.T) {
	t.Parallel()
	// 5665 (Cygnus Loop Panel 2): a 1.047051-day cycle of 0.04 mag that
	// passed the false-period tests while the ZTF row itself found it too
	// faint, on a spot over a hot master dark pixel whose own frames showed
	// no change.
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	pr := &period.Result{Ephemeris: &period.Ephemeris{Period: 1.047051, Kind: period.KindSinusoidal},
		Robust: &period.Robust{Stable: true, CompPass: true, Real: true, Comparisons: 5}}
	ztf := check.Skipped(check.ZTF, "676 g, 1,202 r and 33 i points from 2018 to 2025. Too faint a change here to see the 1.04705 day cycle.")
	groups := []Group{{ID: check.GroupSurveys, Checks: []check.Check{ztf, check.New(check.Fold, check.Pass, ""), check.New(check.Echo, check.Pass, ""), check.New(check.Compare, check.Pass, "")}}}
	s := suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{})
	if s.State != app.VetUnsure {
		t.Errorf("5665: %+v, want unsure", s)
	}
	groups[0].Checks[0] = check.New(check.ZTF, check.Pass, "Rises and falls by 0.08 mag in r every 1.047 days.")
	if s := suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{}); s.State != app.VetReal || s.Kind != KindPeriodic {
		t.Errorf("a cycle ZTF shows: %+v, want real and periodic", s)
	}
}

func TestSuggestion(t *testing.T) {
	t.Parallel()
	night := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	d := 0.58
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}, Best: app.DiscoveryCandidate{Night: night, Sign: -1, DeltaMag: &d}}
	ref := check.New(check.RefClean, check.Fail, "Hot pixels at this spot in S-II and H-a subs from 2025-11-08 and 2025-11-10 went into the reference image.")
	flat := check.New(check.OwnChange, check.Fail, "The star stayed within 0.02 mag of normal.")
	groups := []Group{{ID: check.GroupImage, Checks: []check.Check{ref}}, {ID: check.GroupOwn, Checks: []check.Check{flat}}}
	s := suggest(sum, ownimage.Flags{RefContaminated: true}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, nil, nil, groups, changeOf(sum))
	if s.State != app.VetArtifact || s.Say != "Artifact: reference image contaminated" || s.Confidence != High {
		t.Errorf("reference contaminated %+v", s)
	}
	if s.Why != "The star did not fade. "+ref.Evidence+" That made this spot look fainter on the new nights." {
		t.Errorf("why %q", s.Why)
	}
	s = suggest(sum, ownimage.Flags{}, nil, Known{Listed: "VSX lists it as V0123 And"}, check.New(check.Stays, check.Pass, ""), fetches{}, nil, nil, nil, changeOf(sum))
	if s.State != app.VetKnown || s.Why != "VSX lists it as V0123 And." {
		t.Errorf("known %+v", s)
	}
	e := &period.Ephemeris{Period: 9.98715, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{{Phase: 0}, {Phase: 0.5}}}
	pr := &period.Result{Ephemeris: e, Fits: []period.SurveyFit{{Survey: "ZTF", Agrees: true}, {Survey: "TESS", Agrees: true}},
		Robust: &period.Robust{Stable: true, CompPass: true, Real: true, Comparisons: 5, TESS: true}}
	groups = []Group{{ID: check.GroupSurveys, Checks: []check.Check{
		check.New(check.Fold, check.Pass, ""), check.New(check.Half, check.Pass, ""), check.Skipped(check.Double, ""),
		check.New(check.TESS, check.Pass, ""), check.New(check.OwnTiming, check.Pass, ""),
	}}}
	s = suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, &period.Timing{Dip: 1}, groups, changeOf(sum))
	if s.State != app.VetReal || s.Say != "Looks like a new eclipsing binary, repeats every 9.987 days" || s.Confidence != High || s.Kind != KindEclipsingBinary {
		t.Errorf("binary %+v", s)
	}
}

func TestSurveyFadesVerdict(t *testing.T) {
	t.Parallel()
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	own := check.New(check.OwnChange, check.Pass, "The star faded by 0.21 mag.")
	cases := []struct {
		name  string
		fades *period.Fades
		ztf   check.Status
		want  string
		say   string
	}{
		{"15523 dipper", &period.Fades{Nights: 4, Steady: 4, Stars: 30}, check.Pass, app.VetReal, "Looks like a new variable star, fades now and then"},
		{"field", &period.Fades{Nights: 4, Field: 4, Stars: 30}, check.Fail, app.VetUnsure, "Not sure: nearby stars change with it in ZTF"},
		{"untested", &period.Fades{Nights: 3}, check.Pass, app.VetUnsure, sayNotSure},
	}
	for _, c := range cases {
		pr := &period.Result{Fades: c.fades}
		groups := []Group{{ID: check.GroupOwn, Checks: []check.Check{own}}, {ID: check.GroupSurveys, Checks: []check.Check{check.New(check.ZTF, c.ztf, "")}}}
		s := suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{Fainter: true})
		if s.State != c.want || s.Say != c.say {
			t.Errorf("%s: %+v, want %s %q", c.name, s, c.want, c.say)
		}
		if wantKind := map[string]string{app.VetReal: KindFades}[c.want]; s.Kind != wantKind {
			t.Errorf("%s: kind %q, want %q", c.name, s.Kind, wantKind)
		}
	}
}

func TestTESSOnlyDipsAreArtifact(t *testing.T) {
	t.Parallel()
	// 17189 (Cygnus Loop Panel 2): two 0.09 mag TESS dips every 26.82863
	// days that ZTF and ATLAS, inside the dips on 24 nights, measure at
	// 0.01 mag. The TESS check passed and once made it real.
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	pr := &period.Result{Ephemeris: &period.Ephemeris{Period: 26.828629, Kind: period.KindEclipsing, Eclipses: []period.Eclipse{{Phase: 0, Width: 0.007}}},
		Robust: &period.Robust{Stable: true, CompPass: true, Real: true, TESSOnly: true, TESSDepth: 0.087, GroundDepth: 0.012}}
	groups := []Group{{ID: check.GroupSurveys, Checks: []check.Check{check.New(check.ZTF, check.Fail, ""), check.New(check.ATLAS, check.Fail, ""),
		check.New(check.TESS, check.Pass, ""), check.New(check.Echo, check.Pass, "")}}}
	s := suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{Fainter: true})
	if s.State != app.VetArtifact || s.Say != "Artifact: the dips show only in TESS" {
		t.Errorf("17189: %+v, want the TESS-only artifact", s)
	}
	pr.Robust.TESSOnly, pr.Robust.TESS = false, true
	if s := suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{Fainter: true}); s.State != app.VetReal {
		t.Errorf("dips TESS confirms at the star: %+v, want real", s)
	}
}
