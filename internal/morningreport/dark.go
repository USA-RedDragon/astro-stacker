package morningreport

import (
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

// DarkSunAltitude is the Sun altitude, in degrees, the night is dark at or
// below: the observatory's NINA sequence waits for the Sun to sink to -14°
// before it images and ends the night once the Sun is above -13.9°.
const DarkSunAltitude = -13.9

// DarkSpans are the stretches of [start, end) with the Sun at or below
// DarkSunAltitude at site, their edges found to the second.
func DarkSpans(site sky.Site, start, end time.Time) []Span {
	dark := func(t time.Time) bool { return site.SunAltitude(t) <= DarkSunAltitude }
	edge := func(a, b time.Time) time.Time {
		was := dark(a)
		for b.Sub(a) > time.Second {
			m := a.Add(b.Sub(a) / 2)
			if dark(m) == was {
				a = m
			} else {
				b = m
			}
		}
		return b
	}
	var out []Span
	in := dark(start)
	from, prev := start, start
	for prev.Before(end) {
		t := prev.Add(Step)
		if t.After(end) {
			t = end
		}
		if d := dark(t); d != in {
			e := edge(prev, t)
			if d {
				from = e
			} else {
				out = append(out, Span{From: from, To: e})
			}
			in = d
		}
		prev = t
	}
	if in && from.Before(end) {
		out = append(out, Span{From: from, To: end})
	}
	return out
}

// Total is how long the spans last together, overlaps counted once.
func Total(spans []Span) time.Duration {
	var d time.Duration
	for _, s := range sortSpans(spans) {
		d += s.To.Sub(s.From)
	}
	return d
}

// Overlap is how long a and b cover the same time.
func Overlap(a, b []Span) time.Duration {
	a, b = sortSpans(a), sortSpans(b)
	var d time.Duration
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		from, to := a[i].From, a[i].To
		if b[j].From.After(from) {
			from = b[j].From
		}
		if b[j].To.Before(to) {
			to = b[j].To
		}
		if to.After(from) {
			d += to.Sub(from)
		}
		if a[i].To.Before(b[j].To) {
			i++
		} else {
			j++
		}
	}
	return d
}
