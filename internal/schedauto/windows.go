package schedauto

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	// TypicalNights is how many nights from tonight the typical window is
	// the median over, as for comets and asteroids.
	TypicalNights = 90
	windowStep    = 5 * time.Minute
)

// Nights are the dark times of tonight and the nights after it, worked out
// once for every target.
type Nights struct {
	Site  sky.Site
	First string
	dark  [][]time.Time
}

// NightsFrom works out the astronomical darkness of n nights from the night
// sky.Site.Tonight gives for now.
func NightsFrom(site sky.Site, now time.Time, n int) Nights {
	out := Nights{Site: site, dark: make([][]time.Time, 0, n)}
	night := site.Tonight(now, windowStep)
	for i := range n {
		if i > 0 {
			night = site.NightOf(night.Start.Add(36*time.Hour), windowStep)
		}
		if i == 0 {
			out.First = night.Start.Add(time.Duration(site.Longitude / 15 * float64(time.Hour))).Format(time.DateOnly)
		}
		out.dark = append(out.dark, night.Dark)
	}
	return out
}

// Window is a target's longest stretch of darkness above its limit tonight
// and the median of that over the nights that have one, in hours. Nil is no
// window at all.
type Window struct {
	Tonight *float64 `json:"tonightHours"`
	Typical *float64 `json:"typicalHours"`
	Nights  int      `json:"nightsWithWindow"`
}

func longest(dark []time.Time, above func(time.Time) bool) float64 {
	var run, best time.Duration
	for i, t := range dark {
		if i > 0 && t.Sub(dark[i-1]) > windowStep {
			run = 0
		}
		if above(t) {
			run += windowStep
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best.Hours()
}

func hours(v float64) *float64 {
	r := math.Round(v*100) / 100
	return &r
}

// Median is the median of the positive values, nil when there are none.
func Median(vs []float64) *float64 {
	var pos []float64
	for _, v := range vs {
		if v > 0 {
			pos = append(pos, v)
		}
	}
	if len(pos) == 0 {
		return nil
	}
	sort.Float64s(pos)
	m := pos[len(pos)/2]
	if len(pos)%2 == 0 {
		m = (pos[len(pos)/2-1] + m) / 2
	}
	return hours(m)
}

// FixedWindow is the window of a target at a fixed ra, dec (degrees) above
// limit, which may follow a custom horizon.
func (n Nights) FixedWindow(ra, dec float64, limit mosaics.Limit) Window {
	site := mosaics.Site{Lat: n.Site.Latitude, Lon: n.Site.Longitude}
	above := func(t time.Time) bool {
		alt, az := mosaics.AltAz(t, site, ra, dec)
		return alt >= limit.At(az)
	}
	var w Window
	all := make([]float64, 0, len(n.dark))
	for i, d := range n.dark {
		h := longest(d, above)
		if i == 0 && h > 0 {
			w.Tonight = hours(h)
		}
		if h > 0 {
			w.Nights++
		}
		all = append(all, h)
	}
	w.Typical = Median(all)
	return w
}
