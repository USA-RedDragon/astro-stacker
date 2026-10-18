package binaries

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/eclfacts"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

const (
	// DarkSun is the highest the Sun may be at mid-eclipse, in degrees.
	DarkSun = -12.0
	// MinAltitude is the lowest the star may be at mid-eclipse.
	MinAltitude = 30.0
	// windowDurations is how many eclipse durations an imaging window
	// spans, centred on mid-eclipse: a duration of normal light on each
	// side of the eclipse.
	windowDurations = 3.0
	// windowSigma is how many timing sigmas widen the window on each side.
	windowSigma = 3.0
)

// Predicted is one predicted eclipse: its middle in UTC with its one
// sigma timing error, and how long it lasts.
type Predicted struct {
	Dip      int
	Kind     string
	Mid      time.Time
	Err      time.Duration
	Duration time.Duration
}

// Predict lists the eclipses of e between from and to, the deeper dip
// first at each time: mid-eclipse from the ephemeris (BJD TDB) moved to
// UTC at Earth toward ra, dec, with an error from the epoch and period
// errors grown over the cycles since the epoch.
func Predict(e *period.Ephemeris, deep int, ra, dec float64, from, to time.Time) []Predicted {
	if e == nil || e.Period <= 0 || !to.After(from) {
		return nil
	}
	b0, b1 := lc.BJD(from, ra, dec), lc.BJD(to, ra, dec)
	var out []Predicted
	for k, d := range e.Eclipses {
		kind := KindPrimary
		if k != deep {
			kind = KindSecondary
		}
		for n := math.Floor((b0-e.T0)/e.Period - d.Phase); ; n++ {
			b := e.T0 + (n+d.Phase)*e.Period
			if b > b1 {
				break
			}
			if b < b0 {
				continue
			}
			sig := math.Hypot(e.T0Err, n*e.PeriodErr)
			out = append(out, Predicted{Dip: k, Kind: kind, Mid: lc.UTCOfBJD(b, ra, dec),
				Err: time.Duration(sig * 86400e9), Duration: time.Duration(eclfacts.Duration(e, k) * 86400e9)})
		}
	}
	slices.SortFunc(out, func(a, b Predicted) int { return a.Mid.Compare(b.Mid) })
	return out
}

// Window is the imaging window of an eclipse: windowDurations of its
// duration centred on the middle, widened by windowSigma timing errors.
func (p Predicted) Window() (time.Time, time.Time) {
	half := time.Duration(windowDurations/2*float64(p.Duration)) + time.Duration(windowSigma*float64(p.Err))
	return p.Mid.Add(-half).Truncate(time.Minute), p.Mid.Add(half).Add(time.Minute - 1).Truncate(time.Minute)
}

// Seen is whether the site sees an eclipse's middle: Sun below DarkSun
// and the star above MinAltitude, with a plain note.
func Seen(site sky.Site, ra, dec float64, t time.Time) (bool, string) {
	sun := site.SunAltitude(t)
	alt := site.Altitude(ra, dec, t)
	switch {
	case sun > 0:
		return false, "Sun up"
	case sun > DarkSun:
		return false, "Sky not dark yet"
	case alt < 0:
		return false, "Star below the horizon"
	case alt < MinAltitude:
		return false, fmt.Sprintf("Only %.0f° up", alt)
	}
	return true, fmt.Sprintf("Dark and %.0f° up at mid-eclipse", alt)
}

// Now is where a Gaia star is at t, by its proper motion.
func (g GaiaFacts) Now(t time.Time) (float64, float64) {
	year := 2000 + t.Sub(time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)).Hours()/24/365.25
	return starcat.Propagate(g.RA, g.Dec, g.PMRA, g.PMDec, year-starcat.GaiaEpoch)
}

// J2000 is the star's position at epoch 2000.0, the one VSX asks for.
func (g GaiaFacts) J2000() (float64, float64) {
	return starcat.Propagate(g.RA, g.Dec, g.PMRA, g.PMDec, 2000-starcat.GaiaEpoch)
}

// ZoneNote says which zone the times between from and to are in, and
// when daylight time ends or starts inside them.
func ZoneNote(loc *time.Location, from, to time.Time) string {
	name := loc.String()
	if name == "America/Chicago" {
		name = "US Central"
	}
	a, _ := from.In(loc).Zone()
	b, _ := to.In(loc).Zone()
	if a == b {
		return fmt.Sprintf("Times are %s (%s).", name, a)
	}
	lo, hi := from, to
	for hi.Sub(lo) > time.Minute {
		mid := lo.Add(hi.Sub(lo) / 2)
		if z, _ := mid.In(loc).Zone(); z == a {
			lo = mid
		} else {
			hi = mid
		}
	}
	return fmt.Sprintf("Times are %s (%s until %s, then %s).", name, a, hi.In(loc).Format("Jan 2"), b)
}

// Labels are an instant's day, time and zone in loc: "Sat Oct 17",
// "4:18 AM", "CDT".
func Labels(t time.Time, loc *time.Location) (string, string, string) {
	l := t.Round(time.Minute).In(loc)
	z, _ := l.Zone()
	return l.Format("Mon Jan 2"), l.Format("3:04 PM"), z
}
