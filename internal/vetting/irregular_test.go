package vetting

import (
	"errors"
	"slices"
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

// field35413 holds the ZTF g and r light curves of source 35413 (Gaia DR3
// 369281498567263616, "t") and of six Gaia stars within 4' of it and 0.2
// mag of its G, named by their Gaia DR3 IDs, closest in G first.
const field35413 = "testdata/field_35413.csv.zst"

func field35413Stars() []string {
	return []string{"t", "369280566556918144", "369281116312720000", "369281498567265920", "369284861524315904", "369281601646477824", "369280738355610368"}
}

// fieldVerdict is the vet's suggestion for the star named who in the
// 35413 field, flagged brighter on the user's nights of 2025 October 17,
// 18, 20 and 26 with its own frames confirming the change, and the five
// stars of the field closest to it in G as its comparison stars.
func fieldVerdict(t *testing.T, who string) (Suggestion, *period.Result) {
	t.Helper()
	series, _ := readSeriesAs(t, field35413, who)
	var comps [][]lc.Series
	for _, o := range field35413Stars() {
		if o == who || len(comps) == compStars {
			continue
		}
		c, _ := readSeriesAs(t, field35413, o)
		comps = append(comps, c)
	}
	night17 := "2025-10-17"
	nights := []string{"2025-10-26", "2025-10-20", "2025-10-18", night17, night17}
	ms := make([]app.DiscoveryCandidate, 0, len(nights))
	for _, d := range nights {
		n, err := time.Parse(time.DateOnly, d)
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, app.DiscoveryCandidate{Night: n})
	}
	opts := period.Options{ChangeMag: -0.12, Flagged: flaggedBJDs(ms, 11.1542557, 41.4423365), Comparisons: func() [][]lc.Series { return comps }}
	pr, err := period.Analyze(series, opts)
	if err != nil && !errors.Is(err, period.ErrNoPeriod) {
		t.Fatal(err)
	}
	if pr != nil && !hasPeriod(pr.Ephemeris) {
		pr.Ephemeris = nil
	}
	groups := []Group{
		{ID: check.GroupOwn, Checks: []check.Check{check.New(check.OwnChange, check.Pass, "Over the 12 flagged nights and filters the star was 0.10 mag brighter on average.")}},
		{ID: check.GroupSurveys, Checks: surveyRows(fetches{}, pr, nil)},
	}
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}, Members: ms}
	return suggest(sum, ownimage.Flags{}, nil, Known{}, check.New(check.Stays, check.Pass, ""), fetches{}, pr, nil, groups, change{}), pr
}

// TestIrregular35413 is source 35413 in M31: no period holds up, but its
// ZTF g scatters 0.043 mag beyond its errors while nearby stars of the
// same brightness stay near 0.01, and ZTF has it about 0.12 mag brighter
// in g around the nights the user's frames flagged it brighter. The vet
// once stopped at "Not sure: the 1.002-day cycle does not hold up"; it
// must call it a new irregular variable.
func TestIrregular35413(t *testing.T) {
	t.Parallel()
	s, pr := fieldVerdict(t, "t")
	t.Logf("%s, %s (%s): %s; ephemeris %+v", s.State, s.Say, s.Confidence, s.Why, pr.Ephemeris)
	if s.State != app.VetReal || s.Say != sayIrregular || s.Kind != KindIrregular || s.Confidence != Medium {
		t.Fatalf("got %s, %q, %s (%s)", s.State, s.Say, s.Kind, s.Confidence)
	}
	for _, w := range []string{"ZTF g and r", "brighter than usual", "no steady period"} {
		if !strings.Contains(s.Why, w) {
			t.Errorf("why lacks %q: %s", w, s.Why)
		}
	}
	i := slices.IndexFunc(pr.Excess.Bands, func(b period.ExcessBand) bool { return b.Band == lc.ZTF+" g" })
	if i < 0 {
		t.Fatalf("no g band: %+v", pr.Excess)
	}
	if g := pr.Excess.Bands[i]; g.Excess < 0.035 || g.Excess > 0.05 || g.Flagged > -0.1 || g.Flagged < -0.16 {
		t.Errorf("g band: %+v", g)
	}
}

// TestSteadyFieldStarNotIrregular: the same verdict for steady stars of
// the 35413 field, put in its place with the same flagged nights, is not
// a new irregular variable: one scatters no more than its neighbours,
// the other scatters but ZTF has it no brighter on the flagged nights.
func TestSteadyFieldStarNotIrregular(t *testing.T) {
	t.Parallel()
	for _, who := range []string{"369280566556918144", "369281116312720000"} {
		t.Run(who, func(t *testing.T) {
			t.Parallel()
			s, pr := fieldVerdict(t, who)
			t.Logf("%s, %s: %+v", s.State, s.Say, pr.Excess)
			if s.State == app.VetReal || s.Kind == KindIrregular {
				t.Errorf("got %s, %q: %s", s.State, s.Say, s.Why)
			}
			if pr.Excess.Holds() {
				t.Errorf("excess holds: %+v", pr.Excess)
			}
		})
	}
}
