package vetting

import (
	"errors"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

func ownFailGroups(pr *period.Result) []Group {
	return []Group{
		{ID: check.GroupOwn, Checks: []check.Check{check.New(check.OwnChange, check.Fail, "Your own frames show no change beyond the noise.")}},
		{ID: check.GroupSurveys, Checks: surveyRows(fetches{}, pr, nil)},
	}
}

func ownFailSuggest(pr *period.Result) Suggestion {
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	return suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, ownFailGroups(pr), change{})
}

// ownFailVerdict is the vet's suggestion for an audit case from a fresh
// period search, when the user's own frames did not measure the change.
func ownFailVerdict(t *testing.T, key string) Suggestion {
	t.Helper()
	series, comps := readAuditSeries(t, key)
	pr, err := period.Analyze(series, period.Options{Comparisons: func() [][]lc.Series { return comps }})
	if err != nil && !errors.Is(err, period.ErrNoPeriod) {
		t.Fatal(err)
	}
	if pr != nil && !hasPeriod(pr.Ephemeris) {
		pr.Ephemeris = nil
	}
	return ownFailSuggest(pr)
}

// TestSurveyProvenVariable167 is source 167 (Gaia DR3 387387289682776192):
// the user's frames did not confirm the change, but ZTF g and r show a
// 4.318 day cycle once its 0.2 mag fade of 2022 and 2023 is taken out,
// which TESS sectors 17, 57 and 84 confirm, and no catalog lists it. The
// vet once called it "Artifact: the star did not change"; it must call
// it a new variable star at low confidence, without TESS in the run.
func TestSurveyProvenVariable167(t *testing.T) {
	t.Parallel()
	s := ownFailVerdict(t, "167")
	t.Logf("%s, %s (%s): %s", s.State, s.Say, s.Confidence, s.Why)
	if s.State != app.VetReal || s.Say != "Looks like a new variable star, repeats every 4.318 days" || s.Confidence != Low {
		t.Errorf("got %s, %q (%s)", s.State, s.Say, s.Confidence)
	}
	for _, w := range []string{"next to one day", "season to season", "may not be the star's own"} {
		if !strings.Contains(s.Why, w) {
			t.Errorf("why lacks %q: %s", w, s.Why)
		}
	}
}

// TestFlatSurveysStayArtifacts are audit stars whose surveys show no
// season to season change and no period that passes: a detection the
// user's frames do not confirm stays an artifact.
func TestFlatSurveysStayArtifacts(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"4838", "17073", "28513", "43556"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			s := ownFailVerdict(t, key)
			if s.State != app.VetArtifact || s.Say != "Artifact: the star did not change" {
				t.Errorf("got %s, %q: %s", s.State, s.Say, s.Why)
			}
		})
	}
}

// TestTrendOnlyNotArtifact is a star the surveys prove variable only by
// its season to season change: not an artifact, not yet a new variable
// star. Without the change in two bands it stays an artifact.
func TestTrendOnlyNotArtifact(t *testing.T) {
	t.Parallel()
	band := func(b string, r float64) period.TrendBand {
		return period.TrendBand{Band: b, Range: r, Z: 30, CompRange: 0.01, Comparisons: 5}
	}
	pr := &period.Result{Trend: &period.Trend{Bands: []period.TrendBand{band(lc.ZTF+" g", 0.27), band(lc.ZTF+" r", 0.2)}}}
	s := ownFailSuggest(pr)
	if s.State != app.VetUnsure || s.Say != sayVariesUnconfirmed || s.Confidence != Low || !strings.Contains(s.Why, "0.27 mag in g") {
		t.Errorf("two bands: got %s, %q (%s): %s", s.State, s.Say, s.Confidence, s.Why)
	}
	pr.Trend.Bands = pr.Trend.Bands[:1]
	if s := ownFailSuggest(pr); s.State != app.VetArtifact {
		t.Errorf("one band: got %s, %q", s.State, s.Say)
	}
}
