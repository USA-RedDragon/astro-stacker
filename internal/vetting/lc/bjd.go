package lc

import (
	"math"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
)

var earth = sync.OnceValues(ephem.Load)

const (
	lightKmPerDay = 299792.458 * 86400
	unixJD        = 2440587.5
)

var leapSeconds = []struct {
	from int64
	tai  float64
}{
	{time.Date(2017, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 37},
	{time.Date(2015, 7, 1, 0, 0, 0, 0, time.UTC).Unix(), 36},
	{time.Date(2012, 7, 1, 0, 0, 0, 0, time.UTC).Unix(), 35},
	{time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 34},
	{time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 33},
	{time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 32},
}

func ttMinusUTC(t time.Time) float64 {
	u := t.Unix()
	for _, l := range leapSeconds {
		if u >= l.from {
			return l.tai + 32.184
		}
	}
	return 31 + 32.184
}

func bjd(t time.Time, ra, dec float64) float64 {
	dt := ttMinusUTC(t)
	jdTDB := unixJD + (float64(t.UTC().UnixNano())/1e9+dt)/86400
	e, err := earth()
	if err != nil {
		return jdTDB
	}
	p, err := e.Barycentric(ephem.Earth, ephem.ET(t)-ephem.TTMinusUTC+dt)
	if err != nil {
		return jdTDB
	}
	r, d := ra*math.Pi/180, dec*math.Pi/180
	n := [3]float64{math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)}
	return jdTDB + (p[0]*n[0]+p[1]*n[1]+p[2]*n[2])/lightKmPerDay
}

// HJD is the heliocentric Julian date, on the UTC scale as VSX and most
// observers give it, of a UTC instant seen from Earth's centre toward ra,
// dec in degrees.
func HJD(t time.Time, ra, dec float64) float64 {
	jd := unixJD + float64(t.UTC().UnixNano())/1e9/86400
	e, err := earth()
	if err != nil {
		return jd
	}
	et := ephem.ET(t)
	pe, err := e.Barycentric(ephem.Earth, et)
	if err != nil {
		return jd
	}
	ps, err := e.Barycentric(ephem.Sun, et)
	if err != nil {
		return jd
	}
	r, d := ra*math.Pi/180, dec*math.Pi/180
	n := [3]float64{math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)}
	return jd + ((pe[0]-ps[0])*n[0]+(pe[1]-ps[1])*n[1]+(pe[2]-ps[2])*n[2])/lightKmPerDay
}

// UTCOfBJD is the UTC instant at Earth's centre whose barycentric Julian
// date in TDB toward ra, dec is b.
func UTCOfBJD(b, ra, dec float64) time.Time {
	t := time.Unix(0, int64((b-unixJD)*86400e9)).UTC()
	for range 4 {
		t = t.Add(time.Duration((b - BJD(t, ra, dec)) * 86400e9))
	}
	return t
}
