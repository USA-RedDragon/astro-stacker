package skyevents

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

const (
	rotationStep = 1.0
	// rotationTol is the finest rotator step the fit search looks at.
	rotationTol = 0.01
	// slackTol is the gain in room below which a better rotation is not
	// worth looking for.
	slackTol = 0.002
)

type disc struct {
	ra, dec, radius float64
}

type fit struct {
	center   mosaics.Point
	rotation float64
	slack    float64
}

// shape is a rectangle members must fit in: w along the camera's long side,
// h along its short side, made of cols by rows camera frames. In one frame
// no two members may be further apart edge to edge than the longer side; a
// mosaic takes any arrangement that fits, diagonal ones too.
type shape struct {
	w, h       float64
	cols, rows int
}

func (s shape) reach() float64 {
	if s.cols*s.rows > 1 {
		return math.Hypot(s.w, s.h)
	}
	return math.Max(s.w, s.h)
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

type projected struct {
	c0        mosaics.Point
	xi, eta   []float64
	radius    []float64
	reach     float64
	pairwise  float64
	widestDia float64
}

func project(ds []disc) projected {
	p := projected{c0: meanPoint(ds), xi: make([]float64, len(ds)), eta: make([]float64, len(ds)), radius: make([]float64, len(ds))}
	for i, d := range ds {
		p.xi[i], p.eta[i] = mosaics.Project(p.c0, mosaics.Point{RA: d.ra, Dec: d.dec})
		p.radius[i] = d.radius
		p.reach = math.Max(p.reach, math.Hypot(p.xi[i], p.eta[i]))
		p.widestDia = math.Max(p.widestDia, 2*d.radius)
		for _, o := range ds[:i] {
			p.pairwise = math.Max(p.pairwise, catalog.Separation(d.ra, d.dec, o.ra, o.dec)+d.radius+o.radius)
		}
	}
	return p
}

// box is the room left in a w by h rectangle at a rotation, and the middle
// of the members' extent in frame axes.
func (p projected) box(rotation, w, h float64) (slack, bx, by float64) {
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for i := range p.xi {
		x, y := frameXY(p.xi[i], p.eta[i], rotation)
		r := p.radius[i]
		minX, maxX = math.Min(minX, x-r), math.Max(maxX, x+r)
		minY, maxY = math.Min(minY, y-r), math.Max(maxY, y+r)
	}
	return math.Min(w-(maxX-minX), h-(maxY-minY)), (minX + maxX) / 2, (minY + maxY) / 2
}

func (p projected) centre(rotation, bx, by float64) mosaics.Point {
	lx, ly, ux, uy := axes(rotation)
	return mosaics.Deproject(p.c0, bx*lx+by*ux, bx*ly+by*uy)
}

// fitFrame fits the discs in a w by h frame, where no two may be further
// apart edge to edge than the longer side.
func fitFrame(ds []disc, w, h float64) (fit, bool) {
	return fitRect(ds, w, h, math.Max(w, h))
}

// fitRect finds the rotation that holds every disc inside a w by h
// rectangle with the most room left, and the centre that centres the group
// there; no two discs may be further apart edge to edge than reach. The
// room left changes with rotation no faster than twice the group's reach
// from its middle, so a 1° sweep refined by branch and bound on that bound
// finds every rotation that fits to within rotationTol.
func fitRect(ds []disc, w, h, reach float64) (fit, bool) {
	if len(ds) == 0 {
		return fit{}, false
	}
	p := project(ds)
	if p.widestDia > math.Min(w, h) || p.pairwise > reach {
		return fit{}, false
	}
	lip := 2 * p.reach * deg
	best := fit{slack: math.Inf(-1)}
	var bx, by float64
	try := func(r float64) float64 {
		s, x, y := p.box(r, w, h)
		if s >= 0 && s > best.slack+1e-9 {
			best.slack, best.rotation, bx, by = s, r, x, y
		}
		return s
	}
	n := int(180 / rotationStep)
	vals := make([]float64, n+1)
	for k := range n {
		vals[k] = try(float64(k) * rotationStep)
	}
	vals[n] = vals[0]
	var refine func(a, b, sa, sb float64)
	refine = func(a, b, sa, sb float64) {
		floor := 0.0
		if !math.IsInf(best.slack, -1) {
			floor = best.slack + slackTol
		}
		if (sa+sb)/2+lip*(b-a)/2 < floor || b-a <= rotationTol {
			return
		}
		m := (a + b) / 2
		sm := try(m)
		refine(a, m, sa, sm)
		refine(m, b, sm, sb)
	}
	for k := range n {
		refine(float64(k)*rotationStep, float64(k+1)*rotationStep, vals[k], vals[k+1])
	}
	if math.IsInf(best.slack, -1) {
		return fit{}, false
	}
	best.center = p.centre(best.rotation, bx, by)
	return best, true
}

// fitShapes fits the discs in whichever shape leaves the most room.
func fitShapes(ds []disc, shapes []shape) (fit, int, bool) {
	var best fit
	at, ok := -1, false
	for i, s := range shapes {
		f, fits := fitRect(ds, s.w, s.h, s.reach())
		if fits && (!ok || f.slack > best.slack+1e-9) {
			best, at, ok = f, i, true
		}
	}
	return best, at, ok
}

// centreAt is the centre that centres the discs at a fixed rotation.
func centreAt(ds []disc, rotation float64) mosaics.Point {
	p := project(ds)
	_, bx, by := p.box(rotation, 0, 0)
	return p.centre(rotation, bx, by)
}

func longest(shapes []shape) float64 {
	m := 0.0
	for _, s := range shapes {
		m = math.Max(m, s.reach())
	}
	return m
}
