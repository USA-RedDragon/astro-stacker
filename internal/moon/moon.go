// Package moon places the Moon in the sky for moon avoidance, to a few tenths
// of a degree: the low-precision series of Meeus, Astronomical Algorithms,
// chapters 25 and 47.
package moon

import (
	"math"
	"time"
)

const deg = math.Pi / 180

// synodicMonth is the mean time from new moon to new moon, in days.
const synodicMonth = 29.530588

// Position is where the Moon is at a time.
type Position struct {
	RA, Dec  float64 // geocentric, of date, degrees
	Parallax float64 // horizontal parallax, degrees
	// Age is the days since new moon, from the Moon's elongation: 0 at
	// new, half a synodic month at full.
	Age float64
	// Illumination is the lit fraction of the disc, from the phase angle
	// (Meeus chapter 48).
	Illumination float64
}

func julianDay(t time.Time) float64 {
	return float64(t.UTC().UnixNano())/86400e9 + 2440587.5
}

func sin(d float64) float64 { return math.Sin(d * deg) }
func cos(d float64) float64 { return math.Cos(d * deg) }

// At returns the Moon's position at t.
func At(t time.Time) Position {
	T := (julianDay(t) - 2451545) / 36525
	L := 218.3164477 + 481267.88123421*T // mean longitude
	D := 297.8501921 + 445267.1114034*T  // mean elongation
	M := 357.5291092 + 35999.0502909*T   // Sun's mean anomaly
	Mp := 134.9633964 + 477198.8675055*T // Moon's mean anomaly
	F := 93.2720950 + 483202.0175233*T   // argument of latitude
	lon := L + 6.288774*sin(Mp) + 1.274027*sin(2*D-Mp) + 0.658314*sin(2*D) + 0.213618*sin(2*Mp) -
		0.185116*sin(M) - 0.114332*sin(2*F) + 0.058793*sin(2*D-2*Mp) + 0.057066*sin(2*D-M-Mp) +
		0.053322*sin(2*D+Mp) + 0.045758*sin(2*D-M) - 0.040923*sin(M-Mp) - 0.034720*sin(D) -
		0.030383*sin(M+Mp)
	lat := 5.128122*sin(F) + 0.280602*sin(Mp+F) + 0.277693*sin(Mp-F) + 0.173237*sin(2*D-F) +
		0.055413*sin(2*D-Mp+F) + 0.046271*sin(2*D-Mp-F)
	dist := 385000.56 - 20905.355*cos(Mp) - 3699.111*cos(2*D-Mp) - 2955.968*cos(2*D) - 569.925*cos(2*Mp)
	eps := 23.439291 - 0.0130042*T
	ra := math.Atan2(sin(lon)*cos(eps)-math.Tan(lat*deg)*sin(eps), cos(lon)) / deg
	dec := math.Asin(sin(lat)*cos(eps)+cos(lat)*sin(eps)*sin(lon)) / deg

	// The Sun's longitude, for the elongation.
	sun := 280.46646 + 36000.76983*T + (1.914602-0.004817*T)*sin(M) + 0.019993*sin(2*M)
	elong := math.Mod(lon-sun, 360)
	if elong < 0 {
		elong += 360
	}
	sunDist := (1.00014 - 0.01671*cos(M) - 0.00014*cos(2*M)) * 149597870.7
	psi := math.Acos(cos(lat) * cos(lon-sun))
	phase := math.Atan2(sunDist*math.Sin(psi), dist-sunDist*math.Cos(psi))
	return Position{
		RA: math.Mod(ra+360, 360), Dec: dec,
		Parallax:     math.Asin(6378.14/dist) / deg,
		Age:          elong / 360 * synodicMonth,
		Illumination: (1 + math.Cos(phase)) / 2,
	}
}

// Altitude is the Moon's altitude in degrees seen from latitude lat and east
// longitude lon (degrees), corrected for parallax and without refraction.
func (p Position) Altitude(t time.Time, lat, lon float64) float64 {
	gmst := 280.46061837 + 360.98564736629*(julianDay(t)-2451545)
	ha := gmst + lon - p.RA
	alt := math.Asin(sin(lat)*sin(p.Dec)+cos(lat)*cos(p.Dec)*cos(ha)) / deg
	return alt - p.Parallax*cos(alt)
}

// Separation is the angle in degrees between the Moon and ra, dec.
func (p Position) Separation(ra, dec float64) float64 {
	c := sin(dec)*sin(p.Dec) + cos(dec)*cos(p.Dec)*cos(ra-p.RA)
	return math.Acos(math.Max(-1, math.Min(1, c))) / deg
}

// Avoidance is Target Scheduler's Lorentzian moon avoidance: the separation
// the Moon must keep, distance degrees at full moon, falling off with
// width days either side of it.
func (p Position) Avoidance(distance, width float64) float64 {
	if !(width > 0) {
		return distance
	}
	x := (0.5 - p.Age/synodicMonth) / (width / synodicMonth)
	return distance / (1 + x*x)
}
