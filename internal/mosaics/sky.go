package mosaics

import (
	"math"
	"time"
)

type Site struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

const (
	darkSunAlt = -18
	darkStep   = 5 * time.Minute
)

func julianDay(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

func Sun(t time.Time) (ra, dec float64) {
	T := (julianDay(t) - 2451545) / 36525
	L0 := 280.46646 + 36000.76983*T + 0.0003032*T*T
	M := 357.52911 + 35999.05029*T - 0.0001537*T*T
	C := (1.914602-0.004817*T-0.000014*T*T)*sinD(M) + (0.019993-0.000101*T)*sinD(2*M) + 0.000289*sinD(3*M)
	omega := 125.04 - 1934.136*T
	lambda := L0 + C - 0.00569 - 0.00478*sinD(omega)
	eps := 23.439291 - 0.0130042*T + 0.00256*cosD(omega)
	ra = wrap360(math.Atan2(cosD(eps)*sinD(lambda), cosD(lambda)) / deg)
	dec = math.Asin(sinD(eps)*sinD(lambda)) / deg
	return ra, dec
}

func Altitude(t time.Time, site Site, ra, dec float64) float64 {
	gmst := 280.46061837 + 360.98564736629*(julianDay(t)-2451545)
	ha := gmst + site.Lon - ra
	s := sinD(site.Lat)*sinD(dec) + cosD(site.Lat)*cosD(dec)*cosD(ha)
	return math.Asin(math.Max(-1, math.Min(1, s))) / deg
}

func localNoon(night time.Time, site Site) time.Time {
	y, m, d := night.Date()
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Add(time.Duration(-site.Lon / 15 * float64(time.Hour)))
}

func DarkHours(night time.Time, site Site, points []Point, minAltDeg float64) float64 {
	start := localNoon(night, site)
	steps := int(24 * time.Hour / darkStep)
	dark := 0
	for i := range steps {
		t := start.Add(time.Duration(i) * darkStep)
		if sunRA, sunDec := Sun(t); Altitude(t, site, sunRA, sunDec) >= darkSunAlt {
			continue
		}
		if allAbove(t, site, points, minAltDeg) {
			dark++
		}
	}
	return float64(dark) * darkStep.Hours()
}

func allAbove(t time.Time, site Site, points []Point, minAltDeg float64) bool {
	for _, p := range points {
		if Altitude(t, site, p.RA, p.Dec) <= minAltDeg {
			return false
		}
	}
	return true
}

func MonthlyDarkHours(year int, site Site, points []Point, minAltDeg float64) [12]float64 {
	var out [12]float64
	days := []int{1, 8, 15, 22}
	for m := range 12 {
		sum := 0.0
		for _, d := range days {
			sum += DarkHours(time.Date(year, time.Month(m+1), d, 0, 0, 0, 0, time.UTC), site, points, minAltDeg)
		}
		out[m] = sum / float64(len(days))
	}
	return out
}

func Season(t time.Time, ra float64) (start, end time.Time) {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	target := wrap360(ra + 180)
	diff := func(d int) float64 {
		sunRA, _ := Sun(day.AddDate(0, 0, d).Add(12 * time.Hour))
		return wrap180(sunRA - target)
	}
	best, found := 0, false
	prev := diff(-200)
	for d := -199; d <= 200; d++ {
		cur := diff(d)
		if prev < 0 && cur >= 0 && cur-prev < 10 {
			at := d
			if -prev < cur {
				at = d - 1
			}
			if !found || abs(at) < abs(best) {
				best, found = at, true
			}
		}
		prev = cur
	}
	opp := day.AddDate(0, 0, best)
	return opp.AddDate(0, -6, 0), opp.AddDate(0, 6, 0)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
