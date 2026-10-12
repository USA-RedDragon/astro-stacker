package moonkeep

import (
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

// Candidate is an active target with its planned filters.
type Candidate struct {
	Object  string
	RA, Dec float64
	MinAlt  float64
	Filters []Filter
}

// SlotClass is what the table says at one time for every planned filter of
// one class (broadband or narrowband) on the targets above their minimum
// altitude.
type SlotClass struct {
	Targets  int     `json:"targets"`
	Plans    int     `json:"plans"`
	Skipped  int     `json:"skipped"`
	Unknown  int     `json:"unknown"`
	Skip     bool    `json:"skip"`
	Rate     float64 `json:"rate"`
	RateHigh float64 `json:"rate_high"`
}

// Slot is the Moon and the class summaries at one time of a night. The
// classes are nil when the Moon is down or the Sun is above nautical
// twilight.
type Slot struct {
	At           time.Time  `json:"at"`
	Illumination float64    `json:"illumination"`
	Altitude     float64    `json:"altitude"`
	SunAltitude  float64    `json:"sun_altitude"`
	Broadband    *SlotClass `json:"broadband"`
	Narrowband   *SlotClass `json:"narrowband"`
}

const (
	// NightStep is the spacing of Night's slots.
	NightStep   = 15 * time.Minute
	nauticalSun = -12.0
)

// Night is the table applied to the planned filters of every candidate
// through a night, every NightStep from start to end. A class skips at a
// time when every one of its planned filters on the targets that are up
// then skips, so the scheduler has nothing of that class worth taking. Rate
// and RateHigh are the highest estimate and upper bound among the skipped
// filters. The night quality is not known ahead, so it is not applied.
func Night(m *Model, cands []Candidate, lat, lon float64, start, end time.Time) []Slot {
	var out []Slot
	if m == nil || end.Before(start) || end.Sub(start) > 36*time.Hour {
		return out
	}
	site := sky.Site{Latitude: lat, Longitude: lon}
	for t := start.UTC(); !t.After(end); t = t.Add(NightStep) {
		g := moon.At(t)
		top := g.Topocentric(t, lat, lon)
		sl := Slot{At: t, Illumination: round(Illumination(g.Age)), Altitude: math.Round(top.Altitude(t, lat, lon)*10) / 10,
			SunAltitude: math.Round(site.SunAltitude(t)*10) / 10}
		if sl.Altitude > 0 && sl.SunAltitude <= nauticalSun {
			sl.Broadband = m.slotClass(cands, false, top, site, t, sl.Illumination, sl.Altitude)
			sl.Narrowband = m.slotClass(cands, true, top, site, t, sl.Illumination, sl.Altitude)
		}
		out = append(out, sl)
	}
	return out
}

func (m *Model) slotClass(cands []Candidate, narrowband bool, top moon.Position, site sky.Site, t time.Time, illum, alt float64) *SlotClass {
	out := &SlotClass{}
	for _, c := range cands {
		if site.Altitude(c.RA, c.Dec, t) <= math.Max(0, c.MinAlt) {
			continue
		}
		counted := false
		for _, f := range c.Filters {
			if f.Narrowband != narrowband {
				continue
			}
			if !counted {
				out.Targets++
				counted = true
			}
			out.Plans++
			s := State{Illumination: illum, Altitude: alt, Separation: top.Separation(c.RA, c.Dec)}
			r := m.Lookup(f.Name, f.Narrowband, s)
			switch {
			case r == nil:
				out.Unknown++
			case r.Skip:
				out.Skipped++
				out.Rate = math.Max(out.Rate, r.Rate)
				out.RateHigh = math.Max(out.RateHigh, r.High)
			}
		}
	}
	out.Skip = out.Plans > 0 && out.Skipped == out.Plans
	return out
}
