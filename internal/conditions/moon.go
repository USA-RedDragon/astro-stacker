package conditions

import (
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	synodicMonth = 29.530588
	moonStep     = 10 * time.Minute
	maxMoonSpan  = 36 * time.Hour
)

type MoonPoint struct {
	T   time.Time `json:"t"`
	Alt float64   `json:"alt"`
}

type MoonNight struct {
	Samples      []MoonPoint `json:"samples"`
	Illumination float64     `json:"illumination"`
	Age          float64     `json:"age"`
	Waxing       bool        `json:"waxing"`
	Rises        []time.Time `json:"rises"`
	Sets         []time.Time `json:"sets"`
	NextNew      time.Time   `json:"next_new"`
	NextFull     time.Time   `json:"next_full"`
}

func Moon(lat, lon float64, start, end time.Time) MoonNight {
	if end.Sub(start) > maxMoonSpan {
		end = start.Add(maxMoonSpan)
	}
	out := MoonNight{Samples: []MoonPoint{}, Rises: []time.Time{}, Sets: []time.Time{}}
	var prev *MoonPoint
	for t := start; !t.After(end); t = t.Add(moonStep) {
		p := MoonPoint{T: t.UTC(), Alt: moon.At(t).Altitude(t, lat, lon)}
		if prev != nil {
			if prev.Alt < 0 && p.Alt >= 0 {
				out.Rises = append(out.Rises, crossing(*prev, p))
			} else if prev.Alt >= 0 && p.Alt < 0 {
				out.Sets = append(out.Sets, crossing(*prev, p))
			}
		}
		out.Samples = append(out.Samples, p)
		prev = &out.Samples[len(out.Samples)-1]
	}
	mid := start.Add(end.Sub(start) / 2)
	pos := moon.At(mid)
	out.Age = pos.Age
	out.Illumination = sky.Illumination(pos.Age)
	out.Waxing = pos.Age < synodicMonth/2
	out.NextNew, out.NextFull = nextPhases(start)
	return out
}

func crossing(a, b MoonPoint) time.Time {
	f := -a.Alt / (b.Alt - a.Alt)
	return a.T.Add(time.Duration(f * float64(b.T.Sub(a.T)))).Truncate(time.Minute)
}

func nextPhases(from time.Time) (time.Time, time.Time) {
	var newMoon, full time.Time
	prev := moon.At(from).Age
	for t := from.Add(time.Hour); t.Before(from.Add(31 * 24 * time.Hour)); t = t.Add(time.Hour) {
		age := moon.At(t).Age
		if newMoon.IsZero() && age < prev-synodicMonth/2 {
			newMoon = t.UTC()
		}
		if full.IsZero() && prev < synodicMonth/2 && age >= synodicMonth/2 {
			full = t.UTC()
		}
		if !newMoon.IsZero() && !full.IsZero() {
			break
		}
		prev = age
	}
	return newMoon, full
}
