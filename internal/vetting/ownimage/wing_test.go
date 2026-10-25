package ownimage

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

// wing27673 is 27673's layout (Cygnus Loop, flagged in Red, Green and
// Blue on 2026-09-02 beside a G 14.3 star 7.1 px away, with H-a, O-III
// and S-II frames that night and on 7 far nights): broadSubs subs per
// broadband filter on the flagged night, far-night levels from -0.12 to
// 0.12, and O-III on the flagged night at 1.0 sigma, as the stored vet
// reported, over Blue, Green and Red under 1 sigma, as the audit found.
func wing27673(broadSubs int) ([]Sub, *spotPlan, *ensemble) {
	flagged := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)
	sp := &spotPlan{
		spot:    Spot{Nights: []Night{{Night: flagged, Group: discoveries.GroupBroadband}}},
		flagged: map[string]bool{nightKey(flagged): true},
		bare:    &barePlan{wing: &wingPlan{g: 14.3, sep: 7.1}},
	}
	e := &ensemble{transp: map[int]float64{}}
	var subs []Sub
	add := func(night time.Time, filter string, n int, light float64) {
		for range n {
			e.transp[len(subs)] = 1
			subs = append(subs, Sub{Night: night, Filter: filter})
			sp.recs = append(sp.recs, &rec{wing: light, wingErr: 0.05, wingOK: true})
		}
	}
	filters := []string{synthRed, synthGreen, synthBlue, "H-a", "O-III", "S-II"}
	tonight := map[string]float64{synthRed: 0.01, synthGreen: 0.04, synthBlue: 0.05, "H-a": 0.02, "O-III": 0.127, "S-II": -0.01}
	for _, f := range filters {
		n := 3
		if discoveries.GroupOf(f) == discoveries.GroupBroadband {
			n = broadSubs
		}
		add(flagged, f, n, tonight[f])
	}
	for k := range 7 {
		night := flagged.AddDate(0, 0, -200-30*k)
		lv := 0.04 * float64(k-3)
		for _, f := range filters {
			add(night, f, 3, lv)
		}
	}
	return subs, sp, e
}

func TestWingNightsJudgesTheDetectionsFilterGroup(t *testing.T) {
	t.Parallel()
	subs, sp, e := wing27673(3)
	w := wingNights(subs, sp, e)
	if !w.Measured {
		t.Fatalf("27673's wing not measured with 3 subs per broadband filter: %+v", w)
	}
	switch w.Filter {
	case synthRed, synthGreen, synthBlue:
	default:
		t.Fatalf("27673's wing check named %s, want Red, Green or Blue: %+v", w.Filter, w)
	}
	if w.Filter != synthBlue || w.Sigma >= 1 {
		t.Errorf("27673's wing check gave %s at %.2f sigma, want Blue under 1 sigma", w.Filter, w.Sigma)
	}
}

func TestWingNightsTooFewSubsInGroupIsNotMeasured(t *testing.T) {
	t.Parallel()
	subs, sp, e := wing27673(minPresent - 1)
	if w := wingNights(subs, sp, e); w.Measured || w.Filter != "" {
		t.Errorf("with %d subs per broadband filter the wing was judged in %s: %+v", minPresent-1, w.Filter, w)
	}
}
