package skyevents

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

const rotationStep = 1.0

type disc struct {
	ra, dec, radius float64
}

type fit struct {
	center   mosaics.Point
	rotation float64
	slack    float64
}

func meanPoint(ds []disc) mosaics.Point {
	var x, y, z float64
	for _, d := range ds {
		sr, cr := math.Sincos(d.ra * deg)
		sd, cd := math.Sincos(d.dec * deg)
		x, y, z = x+cd*cr, y+cd*sr, z+sd
	}
	ra := math.Atan2(y, x) / deg
	if ra < 0 {
		ra += 360
	}
	return mosaics.Point{RA: ra, Dec: math.Atan2(z, math.Hypot(x, y)) / deg}
}

func axes(rotation float64) (lx, ly, ux, uy float64) {
	s, c := math.Sincos(rotation * deg)
	return c, -s, s, c
}

// frameXY puts an offset (east, north, degrees) into frame axes: x along the
// long side, y along the short side, for the scheduler's rotation.
func frameXY(east, north, rotation float64) (float64, float64) {
	lx, ly, ux, uy := axes(rotation)
	return east*lx + north*ly, east*ux + north*uy
}

// fitFrame finds a rotation that holds every disc inside a w by h frame,
// keeping the group's widest span within w, and returns the one with the
// most room left and the frame centre that centres the group.
func fitFrame(ds []disc, w, h float64) (fit, bool) {
	if len(ds) == 0 {
		return fit{}, false
	}
	for i := range ds {
		if 2*ds[i].radius > h {
			return fit{}, false
		}
		for j := i + 1; j < len(ds); j++ {
			if catalog.Separation(ds[i].ra, ds[i].dec, ds[j].ra, ds[j].dec)+ds[i].radius+ds[j].radius > w {
				return fit{}, false
			}
		}
	}
	c0 := meanPoint(ds)
	xi := make([]float64, len(ds))
	eta := make([]float64, len(ds))
	for i, d := range ds {
		xi[i], eta[i] = mosaics.Project(c0, mosaics.Point{RA: d.ra, Dec: d.dec})
	}
	best := fit{slack: math.Inf(-1)}
	var bx, by float64
	for r := 0.0; r < 180; r += rotationStep {
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for i, d := range ds {
			x, y := frameXY(xi[i], eta[i], r)
			minX, maxX = math.Min(minX, x-d.radius), math.Max(maxX, x+d.radius)
			minY, maxY = math.Min(minY, y-d.radius), math.Max(maxY, y+d.radius)
		}
		slack := math.Min(w-(maxX-minX), h-(maxY-minY))
		if slack >= 0 && slack > best.slack+1e-9 {
			best.slack, best.rotation = slack, r
			bx, by = (minX+maxX)/2, (minY+maxY)/2
		}
	}
	if math.IsInf(best.slack, -1) {
		return fit{}, false
	}
	lx, ly, ux, uy := axes(best.rotation)
	best.center = mosaics.Deproject(c0, bx*lx+by*ux, bx*ly+by*uy)
	return best, true
}
