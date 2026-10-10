package sky

import (
	"math"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const rad = math.Pi / 180

const (
	AstronomicalTwilight = -18.0
	DefaultStep          = 10 * time.Minute
)

type Site struct {
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	Elevation *float64 `json:"elevation"`
}

func julianDay(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

func SunPosition(t time.Time) (float64, float64) {
	n := julianDay(t) - 2451545
	l := 280.460 + 0.9856474*n
	g := (357.528 + 0.9856003*n) * rad
	lambda := (l + 1.915*math.Sin(g) + 0.020*math.Sin(2*g)) * rad
	eps := (23.439 - 0.0000004*n) * rad
	ra := math.Atan2(math.Cos(eps)*math.Sin(lambda), math.Cos(lambda)) / rad
	dec := math.Asin(math.Sin(eps)*math.Sin(lambda)) / rad
	return math.Mod(ra+360, 360), dec
}

func LST(t time.Time, lon float64) float64 {
	gmst := 280.46061837 + 360.98564736629*(julianDay(t)-2451545)
	return math.Mod(math.Mod(gmst+lon, 360)+360, 360)
}

func AltitudeAtLST(ra, dec, lat, lst float64) float64 {
	ha := (lst - ra) * rad
	s := math.Sin(lat*rad)*math.Sin(dec*rad) + math.Cos(lat*rad)*math.Cos(dec*rad)*math.Cos(ha)
	return math.Asin(math.Max(-1, math.Min(1, s))) / rad
}

func (s Site) Altitude(ra, dec float64, t time.Time) float64 {
	return AltitudeAtLST(ra, dec, s.Latitude, LST(t, s.Longitude))
}

func (s Site) SunAltitude(t time.Time) float64 {
	ra, dec := SunPosition(t)
	return s.Altitude(ra, dec, t)
}

func (s Site) LocalNoon(t time.Time) time.Time {
	offset := time.Duration(s.Longitude / 15 * float64(time.Hour))
	local := t.UTC().Add(offset)
	noon := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.UTC)
	if local.Before(noon) {
		noon = noon.AddDate(0, 0, -1)
	}
	return noon.Add(-offset)
}

type Night struct {
	Start    time.Time     `json:"start"`
	Dusk     *time.Time    `json:"dusk,omitempty"`
	Dawn     *time.Time    `json:"dawn,omitempty"`
	Step     time.Duration `json:"-"`
	Dark     []time.Time   `json:"-"`
	lsts     []float64
	site     Site
	moonFrac float64
	moonMid  moon.Position
}

func (s Site) NightOf(t time.Time, step time.Duration) Night {
	if step <= 0 {
		step = DefaultStep
	}
	start := s.LocalNoon(t)
	n := Night{Start: start, Step: step, site: s}
	for at := start; at.Before(start.Add(24 * time.Hour)); at = at.Add(step) {
		if s.SunAltitude(at) <= AstronomicalTwilight {
			a := at
			if n.Dusk == nil {
				n.Dusk = &a
			}
			n.Dawn = &a
			n.Dark = append(n.Dark, a)
			n.lsts = append(n.lsts, LST(a, s.Longitude))
		}
	}
	mid := start.Add(12 * time.Hour)
	if len(n.Dark) > 0 {
		mid = n.Dark[len(n.Dark)/2]
	}
	n.moonMid = moon.At(mid)
	n.moonFrac = Illumination(n.moonMid.Age)
	return n
}

func Illumination(age float64) float64 {
	return (1 - math.Cos(2*math.Pi*age/29.530588)) / 2
}

func (n Night) DarkHours() float64 {
	return float64(len(n.Dark)) * n.Step.Hours()
}

func (n Night) MoonIllumination() float64 { return n.moonFrac }

func (n Night) MoonSeparation(ra, dec float64) *float64 {
	if len(n.Dark) == 0 {
		return nil
	}
	d := n.moonMid.Separation(ra, dec)
	return &d
}

type Window struct {
	Hours   float64    `json:"hours"`
	Start   *time.Time `json:"start,omitempty"`
	End     *time.Time `json:"end,omitempty"`
	PeakAlt *float64   `json:"peakAlt"`
	PeakAt  *time.Time `json:"peakAt,omitempty"`
}

func (n Night) Window(ra, dec, minAlt float64) Window {
	var w Window
	count := 0
	for i, lst := range n.lsts {
		alt := AltitudeAtLST(ra, dec, n.site.Latitude, lst)
		if w.PeakAlt == nil || alt > *w.PeakAlt {
			w.PeakAlt = &alt
			at := n.Dark[i]
			w.PeakAt = &at
		}
		if alt >= minAlt {
			count++
			at := n.Dark[i]
			if w.Start == nil {
				w.Start = &at
			}
			end := at.Add(n.Step)
			w.End = &end
		}
	}
	w.Hours = float64(count) * n.Step.Hours()
	return w
}

func (n Night) HoursAbove(ra, dec, minAlt float64) float64 {
	count := 0
	for _, lst := range n.lsts {
		if AltitudeAtLST(ra, dec, n.site.Latitude, lst) >= minAlt {
			count++
		}
	}
	return float64(count) * n.Step.Hours()
}

type Year struct {
	Months [12]Night
}

func (s Site) Year(year int, step time.Duration) Year {
	var y Year
	for m := range 12 {
		mid := time.Date(year, time.Month(m+1), 15, 18, 0, 0, 0, time.UTC)
		y.Months[m] = s.NightOf(mid, step)
	}
	return y
}

func (y Year) Hours(ra, dec, minAlt float64) [12]float64 {
	var out [12]float64
	for m := range 12 {
		out[m] = y.Months[m].HoursAbove(ra, dec, minAlt)
	}
	return out
}

func (s Site) Curve(ra, dec float64, from, to time.Time, step time.Duration) []AltitudePoint {
	if step <= 0 {
		step = DefaultStep
	}
	var out []AltitudePoint
	for at := from; !at.After(to); at = at.Add(step) {
		out = append(out, AltitudePoint{At: at, Alt: math.Round(s.Altitude(ra, dec, at)*10) / 10})
	}
	return out
}

type AltitudePoint struct {
	At  time.Time `json:"at"`
	Alt float64   `json:"alt"`
}
