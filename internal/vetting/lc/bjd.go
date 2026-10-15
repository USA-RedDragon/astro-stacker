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

func bjd(t time.Time, ra, dec float64) float64 {
	jdTDB := unixJD + (float64(t.UTC().UnixNano())/1e9+ephem.TTMinusUTC)/86400
	e, err := earth()
	if err != nil {
		return jdTDB
	}
	p, err := e.Barycentric(ephem.Earth, ephem.ET(t))
	if err != nil {
		return jdTDB
	}
	r, d := ra*math.Pi/180, dec*math.Pi/180
	n := [3]float64{math.Cos(d) * math.Cos(r), math.Cos(d) * math.Sin(r), math.Sin(d)}
	return jdTDB + (p[0]*n[0]+p[1]*n[1]+p[2]*n[2])/lightKmPerDay
}
