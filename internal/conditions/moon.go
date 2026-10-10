package conditions

import (
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const (
	synodicMonth = 29.530588
	moonStep     = 10 * time.Minute
	maxMoonSpan  = 36 * time.Hour
	refraction   = 34.0 / 60
	semiDiameter = 0.2725
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
			a, b := aboveHorizon(prev.T, lat, lon), aboveHorizon(p.T, lat, lon)
			if a < 0 && b >= 0 {
				out.Rises = append(out.Rises, bisect(prev.T, p.T, func(t time.Time) bool { return aboveHorizon(t, lat, lon) >= 0 }))
			} else if a >= 0 && b < 0 {
				out.Sets = append(out.Sets, bisect(prev.T, p.T, func(t time.Time) bool { return aboveHorizon(t, lat, lon) < 0 }))
			}
		}
		out.Samples = append(out.Samples, p)
		prev = &out.Samples[len(out.Samples)-1]
	}
	mid := start.Add(end.Sub(start) / 2)
	pos := moon.At(mid)
	out.Age = pos.Age
	out.Illumination = pos.Illumination
	out.Waxing = pos.Age < synodicMonth/2
	out.NextNew, out.NextFull = nextPhases(start)
	return out
}

func aboveHorizon(t time.Time, lat, lon float64) float64 {
	p := moon.At(t)
	return p.Altitude(t, lat, lon) + refraction + semiDiameter*p.Parallax
}

func bisect(lo, hi time.Time, after func(time.Time) bool) time.Time {
	for hi.Sub(lo) > 30*time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if after(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi.Round(time.Minute).UTC()
}

func nextPhases(from time.Time) (time.Time, time.Time) {
	var newMoon, full time.Time
	prevT, prev := from, moon.At(from).Age
	for t := from.Add(time.Hour); t.Before(from.Add(31 * 24 * time.Hour)); t = t.Add(time.Hour) {
		age := moon.At(t).Age
		if newMoon.IsZero() && age < prev-synodicMonth/2 {
			newMoon = bisect(prevT, t, func(x time.Time) bool { return moon.At(x).Age < synodicMonth/2 })
		}
		if full.IsZero() && prev < synodicMonth/2 && age >= synodicMonth/2 {
			full = bisect(prevT, t, func(x time.Time) bool { return moon.At(x).Age >= synodicMonth/2 })
		}
		if !newMoon.IsZero() && !full.IsZero() {
			break
		}
		prevT, prev = t, age
	}
	return newMoon, full
}
