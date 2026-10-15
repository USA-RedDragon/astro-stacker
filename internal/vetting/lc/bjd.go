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
