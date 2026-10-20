package vetting

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/discoverysource"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/ownimage"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// TestDriftingCycleHeldBySeason: a smooth cycle whose folded timing
// drifts is a real semi-regular or spotted star when most seasons find it
// on their own, as 34867's 50.2 days does; dips that drift, echoes of the
// sidereal day or the Moon, and cycles few seasons find stay unsure.
func TestDriftingCycleHeldBySeason(t *testing.T) {
	t.Parallel()
	sum := discoverysource.Summary{Source: app.DiscoverySource{Kind: app.DiscoveryKindChange}}
	stays := check.New(check.Stays, check.Pass, "")
	for _, x := range []struct {
		name       string
		period     float64
		kind       period.Kind
		echo       string
		fits, hits int
		want       string
	}{
		{"34867", 50.18, period.KindSinusoidal, "", 12, 12, app.VetReal},
		{"two thirds", 50.18, period.KindSinusoidal, "", 12, 8, app.VetReal},
		{"few seasons find it", 50.18, period.KindSinusoidal, "", 12, 7, app.VetUnsure},
		{"too few seasons", 50.18, period.KindSinusoidal, "", 3, 3, app.VetUnsure},
		{"drifting dips", 3.97, period.KindEclipsing, "", 12, 12, app.VetUnsure},
		{"sidereal day", 0.99727, period.KindSinusoidal, "one sidereal day", 12, 12, app.VetUnsure},
		{"Moon", 29.53, period.KindSinusoidal, "the Moon's cycle", 12, 12, app.VetUnsure},
	} {
		pr := &period.Result{Ephemeris: &period.Ephemeris{Period: x.period, Kind: x.kind},
			Robust: &period.Robust{Echo: x.echo, Confirmed: "TESS", Stable: true, CompPass: true, Real: true, Comparisons: 5, SeasonFits: x.fits, SeasonHits: x.hits}}
		groups := []Group{{ID: check.GroupSurveys, Checks: []check.Check{
			check.New(check.ZTF, check.Pass, "Rises and falls."), check.New(check.Fold, check.Fail, "The 2021 season is late."),
			check.New(check.Half, check.Pass, ""), check.New(check.Echo, check.Pass, ""), check.New(check.Compare, check.Pass, ""),
		}}}
		s := suggest(sum, ownimage.Flags{}, nil, Known{}, stays, fetches{}, pr, nil, groups, change{})
		if s.State != x.want {
			t.Errorf("%s: want %s, got %s: %s; %s", x.name, x.want, s.State, s.Say, s.Why)
		}
		if s.State == app.VetReal && s.Kind != KindPeriodic {
			t.Errorf("%s: kind %s, want %s", x.name, s.Kind, KindPeriodic)
		}
	}
}
