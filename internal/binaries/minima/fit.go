// Package minima times eclipse minima from one night's photometry and
// reads the trends in a star's timings against its ephemeris.
package minima

import (
	"fmt"
	"maps"
	"math"
	"slices"
)

// Point is one measurement: T in days on the ephemeris's time scale, Y
// in magnitudes (larger is fainter), E its one sigma error and G its
// filter, each filter with its own normal level and depth.
type Point struct {
	T float64
	Y float64
	E float64
	G int
}

// Dip is one predicted eclipse: Mid its predicted middle, Half its half
// width in days, beyond which Shape is 0, and Shape the dip's profile at
// dt days from its middle, 1 there.
type Dip struct {
	Mid   float64
	Half  float64
	Shape func(dt float64) float64
}

// Methods of timing a minimum.
const (
	// MethodModel fits the ephemeris's own dip shape, shifted in time,
	// scaled in depth and on a free normal level.
	MethodModel = "model"
	// MethodKvW is the Kwee and van Woerden method: the time about which
	// the two sides of the dip mirror each other best.
	MethodKvW = "kvw"
)

// Check is a minimum timed by the second method.
type Check struct {
	T      float64 `json:"t"`
	Err    float64 `json:"err"`
	Method string  `json:"method"`
}

// Minimum is one timed eclipse middle, in days, with its one sigma error:
// the larger of the fit's own error and the spread of the times the fit
// gives when its residuals are moved along the night (red noise).
type Minimum struct {
	T        float64 `json:"t"`
	Err      float64 `json:"err"`
	Method   string  `json:"method"`
	Points   int     `json:"points"`
	Before   int     `json:"before"`
	After    int     `json:"after"`
	Depth    float64 `json:"depth"`
	DepthErr float64 `json:"depth_err"`
	// Slope says a straight line was fitted under the dip, as the night's
	// trend asked for it.
	Slope bool `json:"slope"`
	// Width is the night's dip width against the ephemeris's.
	Width  float64 `json:"width"`
	Formal float64 `json:"formal"`
	Bead   float64 `json:"bead"`
	// Red is how much more the residuals' averages over a few frames
	// scatter than white noise would; the formal error is widened by it.
	Red float64 `json:"red"`
	// Drift is how far the middle moves when the night is fitted again
	// with a plausible slow curved drift forced in, either way: a
	// systematic error added to the statistical one.
	Drift float64 `json:"drift"`
	// Check is the second method's time, when the points allow it, and
	// Agree whether it lands where the two methods' chance spread at the
	// fit, Spread, allows; with no Spread, within three of the combined
	// errors.
	Check  *Check  `json:"check,omitempty"`
	Spread *Spread `json:"spread,omitempty"`
	Agree  bool    `json:"agree"`
}

const (
	// errFloor is the smallest error a point is taken to have, in mag.
	errFloor = 0.002
	// searchShare is how far the middle is looked for either side of the
	// prediction, as a share of the half width.
	searchShare = 0.5
	// windowShare is how far points are taken either side of the
	// prediction, as a share of the half width, beyond the search.
	windowShare = 1.5
	gridSteps   = 800
	// inDip is the profile level above which a point counts as in the
	// dip.
	inDip = 0.05
	// minSide is the fewest points in the dip on each side of the middle
	// and minInDip the fewest in all; minGroup the fewest a filter needs
	// to take part.
	minSide  = 2
	minInDip = 6
	minGroup = 4
	// minDepthZ is how clear of its error the fitted depth must be.
	minDepthZ = 5.0
	// slopeP is the chance level below which a straight line is fitted
	// under the dip; slopeMin the fewest points to try it.
	slopeP   = 0.01
	slopeMin = 10
	// baseLevel is the profile level under which a point counts as
	// outside the dip, and baseMin the fewest such points on each side
	// of the middle, in each filter, to trust a straight line under it.
	baseLevel = 0.01
	baseMin   = 3
	// beadMin is the fewest points to move the residuals along, beadShifts
	// the most moves made, each refitted over beadReach formal errors
	// either side of the best middle in beadSteps.
	beadMin    = 8
	beadShifts = 60
	beadReach  = 10.0
	beadSteps  = 200
	// trimRounds is how many times the points are cut to a window
	// centred on the fitted middle, so a dip shape that is a little off
	// pulls both sides alike; reachLevel is the profile level the fitted
	// dip must have come back up to at that window's edge, so the window
	// holds both of its sides, judged by the width fitted.
	trimRounds = 3
	reachLevel = 0.75
	// agreeSigma is how many combined errors the two methods may differ.
	agreeSigma = 3.0
	// clearDepth is how many of its errors a filter's depth must clear for
	// its points to go to the second method.
	clearDepth = 3.0
	// driftCurve is the curvature of the slow drift, in mag a day squared,
	// a night's light may follow unseen, as differential extinction does
	// across an airmass curve: 0.004 mag per half width squared of a dip
	// 0.111 days in half width, about 0.0006 mag an hour squared.
	driftCurve = 0.004 / (0.111 * 0.111)
	// driftMost is the most that drift may add over the frames' reach
	// from the middle, in mag, so a long night is not given more drift
	// than airmass gives.
	driftMost = 0.02
	// evenShare is how far off the middle the night's centre may lie, as
	// a share of its half span, and how far the frames in the dip either
	// side of the middle may differ in number, as a share of them, for the
	// night to count as covering the eclipse evenly.
	evenShare = 0.15
)

// Why a night gave no minimum.
const (
	ReasonFew      = "too few frames near the eclipse"
	ReasonSides    = "the frames did not cover both sides of the eclipse's middle"
	ReasonShallow  = "the dip was not clear of the noise"
	ReasonEdge     = "the best middle fell at the edge of where it was looked for"
	ReasonLoose    = "the middle could not be pinned down"
	ReasonNoFit    = "the eclipse shape could not be fitted"
	ReasonNoBranch = "too few frames on the sides of the dip"
	ReasonNoShape  = "the ephemeris gives this eclipse no width or shape"
	ReasonNoCover  = "none of the measured frames fell inside the dip"
	ReasonNoDip    = "the star showed no dip near the predicted time"
	ReasonDrift    = "the star's light drifted over the night, and the frames outside the dip could not tell the drift from the dip's shape"
	ReasonCurve    = "a slow drift across the night could move the middle"
)

// failure is why a fit found no dip.
type failure int

const (
	fitDone failure = iota
	// noCover is no middle in reach putting points of any filter in the
	// dip.
	noCover
	// noDip is every middle in reach giving the filters a mean depth
	// that is not above 0: a brightening or no dip at all.
	noDip
	// noSolve is the fit's equations having no solution.
	noSolve
)

// worse is the more telling of two failures.
func worse(a, b failure) failure {
	if a == fitDone || (b != fitDone && b < a) {
		return b
	}
	return a
}

type fitOut struct {
	tau, formal  float64
	depth, dErr  float64
	chi          float64
	k            int
	n            int
	betas        [][]float64
	vds          []float64
	model, resid []float64
	// chis is chi-square at each middle stepped, h apart.
	chis []float64
	h    float64
}

// gather is the points within reach of mid in time order, each error at
// least errFloor, the filters with fewer than minGroup points left out
// and the rest numbered from 0, with how many filters are left.
func gather(pts []Point, mid, reach float64) ([]Point, int) {
	byGroup := map[int][]Point{}
	for _, p := range pts {
		if math.Abs(p.T-mid) <= reach && finite(p.T) && finite(p.Y) {
			e := p.E
			if !(e > 0) || !finite(e) {
				e = 0.01
			}
			byGroup[p.G] = append(byGroup[p.G], Point{T: p.T, Y: p.Y, E: math.Max(e, errFloor), G: p.G})
		}
	}
	var use []Point
	ng := 0
	for _, g := range slices.Sorted(maps.Keys(byGroup)) {
		if len(byGroup[g]) < minGroup {
			continue
		}
		for _, p := range byGroup[g] {
			p.G = ng
			use = append(use, p)
		}
		ng++
	}
	slices.SortFunc(use, func(a, b Point) int { return cmpF(a.T, b.T) })
	return use, ng
}

// Time fits the minimum of d in pts: one middle for every filter, each
// filter (G) with its own normal level and depth. ok is false with a
// reason in plain words when the points do not pin a minimum down.
func Time(pts []Point, d Dip) (Minimum, string, bool) {
	if d.Half <= 0 || d.Shape == nil {
		return Minimum{}, ReasonNoShape, false
	}
	span := searchShare * d.Half
	use, ng := gather(pts, d.Mid, windowShare*d.Half+span)
	if len(use) < minInDip+2 {
		return Minimum{}, ReasonFew, false
	}
	on := frameSpan{first: use[0].T, last: use[len(use)-1].T}
	gs := widthGrids(d, span)
	best, fd, slope, fail := fitBest(use, ng, gs)
	if fail != fitDone {
		return Minimum{}, failReason(fail, use, ng, d), false
	}
	for range trimRounds {
		mid := d.Mid + best.tau
		r := math.Min(mid-use[0].T, use[len(use)-1].T-mid)
		if r < reachOf(fd) {
			return Minimum{T: mid, Width: fd.Half / d.Half}, sidesOr(best, reach(mid, use, fd)), false
		}
		trimmed := use[:0:0]
		for _, p := range use {
			if math.Abs(p.T-mid) <= r {
				trimmed = append(trimmed, p)
			}
		}
		if len(trimmed) == len(use) {
			break
		}
		trimmed, ng = regroup(trimmed)
		if len(trimmed) < minInDip+2 || ng == 0 {
			return Minimum{T: mid}, sidesOr(best, ReasonSides), false
		}
		use = trimmed
		if best, fd, slope, fail = fitBest(use, ng, gs); fail != fitDone {
			return Minimum{}, failReason(fail, use, ng, d), false
		}
	}
	m := Minimum{T: d.Mid + best.tau, Method: MethodModel, Points: best.n, Depth: best.depth, DepthErr: best.dErr, Slope: slope, Formal: best.formal,
		Width: fd.Half / d.Half}
	for _, p := range use {
		if fd.Shape(p.T-m.T) <= inDip {
			continue
		}
		if p.T < m.T {
			m.Before++
		} else {
			m.After++
		}
	}
	switch {
	case m.Before < minSide || m.After < minSide:
		return m, fmt.Sprintf("%s (%d frames in the dip before the fitted middle and %d after; each side needs %d)", ReasonSides, m.Before, m.After, minSide), false
	case m.Before+m.After < minInDip:
		return m, ReasonNoBranch, false
	case !(m.Depth > minDepthZ*m.DepthErr):
		return m, shallow(m.Depth, m.DepthErr), false
	case math.Abs(best.tau) > 0.95*span:
		return m, fmt.Sprintf("%s (%.0f minutes %s the predicted middle, where it is looked for up to %.0f minutes either side)",
			ReasonEdge, math.Abs(best.tau)*1440, beforeAfter(best.tau), span*1440), false
	case !(best.formal > 0) || best.formal >= span:
		return m, ReasonLoose, false
	}
	if slope {
		if why := pinned(use, ng, fd, best); why != "" {
			return m, why, false
		}
	}
	beadDiffs := m.addNoise(use, ng, fd, span, slope, best)
	if why := m.addDrift(use, ng, gs, fd, best, on); why != "" {
		return m, why, false
	}
	c, ok := KvW(normalized(use, best), m.T, fd.Half)
	if !ok {
		m.Agree = true
		return m, "", true
	}
	m.Check = &c
	if s, ok := checkSpread(use, ng, d, fd, span, slope, best, m.Red); ok {
		if len(beadDiffs) >= 3 {
			s.BeadSD = stddev(beadDiffs)
		}
		m.Spread = &s
		m.Agree = agrees(m.T-c.T, s)
	} else {
		m.Agree = math.Abs(c.T-m.T) <= agreeSigma*math.Hypot(c.Err, m.Err)
	}
	return m, "", true
}

// addNoise sets m's red noise error and factor when there are beadMin
// points or more, giving the fitted middle less the second method's on
// each night bead moved.
func (m *Minimum) addNoise(use []Point, ng int, fd Dip, span float64, slope bool, best fitOut) []float64 {
	if len(use) < beadMin {
		return nil
	}
	var diffs []float64
	m.Bead, diffs = bead(use, ng, fd, span, slope, best)
	m.Red = redFactor(use, best.resid)
	return diffs
}

// addDrift sets m's drift systematic and its error, the statistical
// error and the drift added in quadrature, giving why the timing is
// refused when the drift moves the middle further than the statistical
// error on a night whose frames do not lie evenly about it, else "".
func (m *Minimum) addDrift(use []Point, ng int, gs []*shapeGrid, fd Dip, best fitOut, on frameSpan) string {
	stat := math.Max(m.Formal*math.Max(1, m.Red), m.Bead)
	m.Drift = driftShift(use, ng, gs, best, on.centre())
	m.Err = math.Hypot(stat, m.Drift)
	if m.Drift > stat && !on.even(use, fd, m.T) {
		return fmt.Sprintf("%s by %.1f minutes; the frames do not cover the eclipse evenly enough to rule it out", ReasonCurve, m.Drift*1440)
	}
	return ""
}

// frameSpan is the first and last frame within reach of the predicted
// middle.
type frameSpan struct{ first, last float64 }

func (n frameSpan) centre() float64 { return (n.first + n.last) / 2 }

// even is whether the night's frames lie evenly about mid: its centre
// within evenShare of its half span of mid, and the frames in use in the
// fitted dip fd differing in number either side by no more than
// evenShare of them.
func (n frameSpan) even(use []Point, fd Dip, mid float64) bool {
	if math.Abs(n.centre()-mid) > evenShare*(n.last-n.first)/2 {
		return false
	}
	var dip [2]int
	for _, p := range use {
		if fd.Shape(p.T-mid) <= inDip {
			continue
		}
		if p.T < mid {
			dip[0]++
		} else {
			dip[1]++
		}
	}
	return math.Abs(float64(dip[0]-dip[1])) <= evenShare*float64(dip[0]+dip[1])
}

// driftShift is the larger shift of the middle when the night is fitted
// again, width and line free as before, with a parabola forced in either
// way: driftCurve about vertex, the night's centre, as an airmass curve
// peaks near the middle of a run of frames, held to driftMost at the
// frame furthest from it. On frames covering the eclipse evenly the
// vertex lies near the middle, both sides feel the drift alike and it
// barely moves the middle; on lopsided frames the drift tilts the dip.
func driftShift(use []Point, ng int, gs []*shapeGrid, best fitOut, vertex float64) float64 {
	var far float64
	for _, p := range use {
		far = math.Max(far, math.Abs(p.T-vertex))
	}
	if !(far > 0) {
		return 0
	}
	q := math.Min(driftCurve, driftMost/(far*far))
	moved := make([]Point, len(use))
	var most float64
	for _, sign := range []float64{1, -1} {
		for i, p := range use {
			x := p.T - vertex
			moved[i] = p
			moved[i].Y = p.Y - sign*q*x*x
		}
		f, _, _, fail := fitBest(moved, ng, gs)
		if fail != fitDone {
			continue
		}
		most = math.Max(most, math.Abs(f.tau-best.tau))
	}
	return most
}

// pinned is why the straight line a fit took under the dip cannot be
// trusted, or "": each filter with points in the dip needs baseMin
// frames outside the dip on each side of its middle, and the slope those
// frames give alone must lie within agreeSigma of their error, widened
// by the reduced chi-square, of the fit's. Else the line may stand in
// for a dip shape the ephemeris's does not match, and it moves the
// middle with it.
func pinned(pts []Point, ng int, d Dip, best fitOut) string {
	mid := d.Mid + best.tau
	red := math.Max(1, best.chi/float64(best.n-best.k))
	for g := range ng {
		if math.IsInf(best.vds[g], 1) {
			continue
		}
		var sw, sx, sy, sxx, sxy float64
		before, after := 0, 0
		for _, p := range pts {
			if p.G != g || d.Shape(p.T-mid) > baseLevel {
				continue
			}
			if p.T < mid {
				before++
			} else {
				after++
			}
			w, x := 1/(p.E*p.E), p.T-d.Mid
			sw += w
			sx += w * x
			sy += w * p.Y
			sxx += w * x * x
			sxy += w * x * p.Y
		}
		if before < baseMin || after < baseMin {
			return fmt.Sprintf("%s (%d frames outside the dip before its middle and %d after; each side needs %d)", ReasonDrift, before, after, baseMin)
		}
		den := sw*sxx - sx*sx
		if !(den > 0) {
			return ReasonDrift
		}
		slope := (sw*sxy - sx*sy) / den
		if e := math.Sqrt(red * sw / den); math.Abs(slope-best.betas[g][2]) > agreeSigma*e {
			return fmt.Sprintf("%s (the frames outside the dip drift %.2f ± %.2f mag a day, the fit under it %.2f)", ReasonDrift, slope, e, best.betas[g][2])
		}
	}
	return ""
}

// sidesOr is why a fit whose window could not be centred on its middle
// gave no minimum: the dip not being clear of the noise, when it is not,
// else sides, the frames missing one side.
func sidesOr(best fitOut, sides string) string {
	if !(best.depth > minDepthZ*best.dErr) {
		return shallow(best.depth, best.dErr)
	}
	return sides
}

// reachOf is how far from its middle the fitted dip fd comes back up to
// reachLevel of its depth.
func reachOf(fd Dip) float64 {
	lo, hi := 0.0, fd.Half
	for range 40 {
		if m := (lo + hi) / 2; fd.Shape(m) > reachLevel {
			lo = m
		} else {
			hi = m
		}
	}
	return hi
}

// reach is why a fit whose frames do not reach reachOf the fitted dip
// either side of its middle gave no minimum, in minutes: a timing then
// rests on one side of the dip and the width it is given.
func reach(mid float64, use []Point, fd Dip) string {
	return fmt.Sprintf("%s (the frames reach %.0f minutes before and %.0f minutes after the fitted middle; each side needs %.0f, where the fitted dip has come a quarter of the way back up)",
		ReasonSides, math.Max(0, mid-use[0].T)*1440, math.Max(0, use[len(use)-1].T-mid)*1440, reachOf(fd)*1440)
}

func shallow(depth, err float64) string {
	return fmt.Sprintf("%s (%.2f ± %.2f mag deep, under %.0f times its error)", ReasonShallow, depth, err, minDepthZ)
}

func beforeAfter(tau float64) string {
	if tau < 0 {
		return "before"
	}
	return "after"
}

// failReason is a failure in plain words, with the depth fitted at the
// predicted middle when the star showed no dip.
func failReason(f failure, use []Point, ng int, d Dip) string {
	switch f {
	case noCover:
		return ReasonNoCover
	case noDip:
		if dep, e, ok := depthAt(use, ng, d, 0); ok {
			return fmt.Sprintf("%s (at the predicted middle it was %.2f ± %.2f mag fainter than its normal level)", ReasonNoDip, dep, e)
		}
		return ReasonNoDip
	case fitDone, noSolve:
	}
	return ReasonNoFit
}

// depthAt is the filters' mean depth and its error for the dip's middle
// at d.Mid+tau, on a flat normal level.
func depthAt(pts []Point, ng int, d Dip, tau float64) (float64, float64, bool) {
	betas, vds, _, ok := solveAt(pts, ng, d, tau, false)
	if !ok {
		return 0, 0, false
	}
	return meanDepth(betas, vds, 1)
}

// meanDepth is the filters' depths averaged by their variances vds times
// red, with its error; filters holding no point in the dip have infinite
// variance and do not count.
func meanDepth(betas [][]float64, vds []float64, red float64) (float64, float64, bool) {
	var sw, sws float64
	for g, b := range betas {
		if !(vds[g] > 0) || math.IsInf(vds[g], 1) {
			continue
		}
		w := 1 / (vds[g] * red)
		sw += w
		sws += w * b[1]
	}
	if !(sw > 0) {
		return 0, 0, false
	}
	return sws / sw, 1 / math.Sqrt(sw), true
}

// widthScales gives the widths fitBest fits the dip at, as shares of the
// ephemeris's: a night's dip can look half as wide or twice as wide as
// the surveys' mean shape, and a dip fitted too narrow moves its middle
// toward the side the frames cover best.
func widthScales() []float64 {
	return []float64{0.5, 0.6, 0.7, 0.8, 0.9, 1, 1.12, 1.25, 1.4, 1.6, 1.8, 2.0}
}

// fitBest fits the dip at each width in widthScales of the ephemeris's
// dip, on a flat normal level and, with enough points, with a straight
// line under it, and keeps the width that fits best. The line is taken
// only when an F test at slopeP finds the best fit with a line better
// than the best without one at any width, so a line cannot stand in for
// a wrong width. The middle's error is read from chi-square at each
// middle at its least over the widths, so a middle that another width
// fits about as well widens it.
func fitBest(use []Point, ng int, gs []*shapeGrid) (fitOut, Dip, bool, failure) {
	var flats, lines []widthFit
	why := fitDone
	span := gs[0].span()
	for _, g := range gs {
		ds := g.d
		fs := fitShapes(use, ng, g, false, len(use) >= slopeMin)
		f, fail := fs[0].f, fs[0].fail
		if fail != fitDone {
			why = worse(why, fail)
			continue
		}
		flats = append(flats, widthFit{f, ds})
		if len(use) < slopeMin {
			continue
		}
		if l, fail := fs[1].f, fs[1].fail; fail == fitDone && l.n > l.k {
			lines = append(lines, widthFit{l, ds})
		}
	}
	if len(flats) == 0 {
		return fitOut{}, Dip{}, false, why
	}
	chosen, slope := flats, false
	if len(lines) > 0 {
		bf, bl := flats[bestOf(flats)].f, lines[bestOf(lines)].f
		f := (bf.chi - bl.chi) / float64(bl.k-bf.k) / (bl.chi / float64(bl.n-bl.k))
		if FProb(f, float64(bl.k-bf.k), float64(bl.n-bl.k)) < slopeP {
			chosen, slope = lines, true
		}
	}
	i := bestOf(chosen)
	best := chosen[i].f
	best.formal = math.Max(best.formal, profileErr(chosen, best, span))
	return best, chosen[i].d, slope, fitDone
}

// scaled is d s times as wide.
func scaled(d Dip, s float64) Dip {
	return Dip{Mid: d.Mid, Half: d.Half * s, Shape: func(dt float64) float64 { return d.Shape(dt / s) }}
}

// widthFit is the dip fitted at one width.
type widthFit struct {
	f fitOut
	d Dip
}

func bestOf(fs []widthFit) int {
	b := 0
	for i, f := range fs {
		if f.f.chi < fs[b].f.chi {
			b = i
		}
	}
	return b
}

// profileErr is the middle's error with the width free: half the range
// of middles where chi-square, at its least over the widths fitted,
// stays within the reduced chi-square, at least 1, of the best fit's.
// The range reaching the edge of the search gives span.
func profileErr(fs []widthFit, best fitOut, span float64) float64 {
	prof := slices.Clone(fs[0].f.chis)
	for _, f := range fs[1:] {
		for j, c := range f.f.chis {
			prof[j] = math.Min(prof[j], c)
		}
	}
	lim := best.chi + math.Max(1, best.chi/float64(best.n-best.k))
	lo, hi := -1, -1
	for j, c := range prof {
		if c <= lim {
			if lo < 0 {
				lo = j
			}
			hi = j
		}
	}
	if lo <= 0 || hi < 0 || hi >= len(prof)-1 {
		return span
	}
	left := float64(lo) - edgeShare(prof[lo-1], prof[lo], lim)
	right := float64(hi) + edgeShare(prof[hi+1], prof[hi], lim)
	return (right - left) * best.h / 2
}

// edgeShare is how far past the last grid step within lim, toward the
// next step at out, chi-square reaches lim.
func edgeShare(out, in, lim float64) float64 {
	if !finite(out) || out <= in {
		return 0
	}
	return (lim - in) / (out - in)
}

// regroup leaves out the filters with fewer than minGroup points and
// numbers the rest from 0 in their order, with how many are left.
func regroup(pts []Point) ([]Point, int) {
	count := map[int]int{}
	for _, p := range pts {
		count[p.G]++
	}
	num := map[int]int{}
	for _, g := range slices.Sorted(maps.Keys(count)) {
		if count[g] >= minGroup {
			num[g] = len(num)
		}
	}
	out := make([]Point, 0, len(pts))
	for _, p := range pts {
		if g, ok := num[p.G]; ok {
			p.G = g
			out = append(out, p)
		}
	}
	return out, len(num)
}

// normalized puts the points of every filter whose depth is clear of
// its error on one dip of depth 1 over its normal level, by the best
// fit, for the second method. Any straight line the fit took is left
// in, so a line that soaked up a wrong dip shape cannot carry the second
// method along with it.
func normalized(pts []Point, f fitOut) []Point {
	out := make([]Point, 0, len(pts))
	for _, p := range pts {
		b := f.betas[p.G]
		if !(b[1] > clearDepth*math.Sqrt(f.vds[p.G])) {
			continue
		}
		out = append(out, Point{T: p.T, Y: (p.Y - b[0]) / b[1], E: p.E / b[1]})
	}
	return out
}

// fitShapeIn steps the dip's middle over g's middles, solving each
// of the ng filters' normal level, depth and, with slope, a straight
// line at each step, and takes the best of the steps where the filters'
// mean depth is above 0: one filter the star is lost in may come out
// with a depth below 0 without forcing the middle, and a filter with no
// point in the dip at a step has its depth held at 0 there. Its error
// is where chi-square rises by the reduced chi-square, at least 1,
// either side.
func fitShapeIn(pts []Point, ng int, g *shapeGrid, slope bool) (fitOut, failure) {
	r := fitShapes(pts, ng, g, slope)
	return r[0].f, r[0].fail
}

// shapeFit is one of fitShapes' fits.
type shapeFit struct {
	f    fitOut
	fail failure
}

// fitShapes is fitShapeIn for each of slopes, the dip's terms summed
// once at each step for all of them.
func fitShapes(pts []Point, ng int, g *shapeGrid, slopes ...bool) []shapeFit {
	type lane struct {
		i      int
		slope  bool
		st     steady
		sums   []normal
		chis   []float64
		bi     int
		solved bool
	}
	n := len(pts)
	out := make([]shapeFit, len(slopes))
	var lanes []*lane
	for i, slope := range slopes {
		per := 2
		if slope {
			per = 3
		}
		if n <= per*ng || g.hi <= g.lo || ng == 0 {
			out[i].fail = noSolve
			continue
		}
		lanes = append(lanes, &lane{i: i, slope: slope, st: newSteady(pts, g.d.Mid, ng, slope), sums: make([]normal, ng), chis: make([]float64, g.steps+1), bi: -1})
	}
	if len(lanes) == 0 {
		return out
	}
	rows := make([][]float64, n)
	for i, p := range pts {
		rows[i] = g.row(p.T)
	}
	terms := make([]dipTerms, ng)
	w, x := lanes[0].st.w, lanes[0].st.x
	for j := range g.steps + 1 {
		addDip(pts, rows, j, w, x, terms)
		for _, l := range lanes {
			l.st.with(terms, l.sums, l.slope)
			chi, ok, dip := chiOf(l.sums, l.slope)
			l.solved = l.solved || ok
			if !ok || !dip {
				l.chis[j] = math.Inf(1)
				continue
			}
			l.chis[j] = chi
			if l.bi < 0 || chi < l.chis[l.bi] {
				l.bi = j
			}
		}
	}
	for _, l := range lanes {
		out[l.i].f, out[l.i].fail = finishShape(pts, ng, g, l.slope, l.chis, l.bi, l.solved)
	}
	return out
}

// finishShape is fitShapeIn's fit from chi-square at each of g's middles
// in chis, the least at bi, solved saying whether any step solved.
func finishShape(pts []Point, ng int, g *shapeGrid, slope bool, chis []float64, bi int, solved bool) (fitOut, failure) {
	per := 2
	if slope {
		per = 3
	}
	k := per * ng
	n := len(pts)
	d, lo, hi, steps, h := g.d, g.lo, g.hi, g.steps, g.h
	span := (hi - lo) / 2
	if bi < 0 {
		if solved {
			return fitOut{}, noDip
		}
		return fitOut{}, noCover
	}
	grid := lo + float64(bi)*h
	tau := grid
	if bi > 0 && bi < steps {
		c0, c1, c2 := chis[bi-1], chis[bi], chis[bi+1]
		if den := c0 - 2*c1 + c2; den > 0 && finite(den) {
			tau += h * 0.5 * (c0 - c2) / den
		}
	}
	betas, vds, chi, ok := solveAt(pts, ng, d, tau, slope)
	if dep, _, okD := meanDepth(betas, vds, 1); !ok || !okD || !(dep > 0) {
		tau = grid
		betas, vds, chi, ok = solveAt(pts, ng, d, tau, slope)
		if dep, _, okD := meanDepth(betas, vds, 1); !ok || !okD || !(dep > 0) {
			return fitOut{}, noSolve
		}
	}
	red := math.Max(1, chi/float64(n-k))
	depth, dErr, _ := meanDepth(betas, vds, red)
	lim := chi + red
	cl, okLo := crossing(chis, bi, -1, lim)
	ch, okHi := crossing(chis, bi, 1, lim)
	formal := span
	if okLo && okHi {
		formal = (ch - cl) * h / 2
	}
	out := fitOut{tau: tau, formal: formal, depth: depth, dErr: dErr, chi: chi, k: k, n: n, betas: betas, vds: vds, chis: chis, h: h}
	out.model = make([]float64, n)
	out.resid = make([]float64, n)
	for i, p := range pts {
		out.model[i] = modelAt(betas[p.G], p, d, tau, slope)
		out.resid[i] = p.Y - out.model[i]
	}
	return out, fitDone
}

// crossing is the fractional grid index where chis first reaches lim
// going from i in direction dir.
func crossing(chis []float64, i, dir int, lim float64) (float64, bool) {
	for j := i; j+dir >= 0 && j+dir < len(chis); j += dir {
		a, b := chis[j], chis[j+dir]
		if b >= lim {
			if !finite(b) || b == a {
				return float64(j + dir), true
			}
			return float64(j) + float64(dir)*(lim-a)/(b-a), true
		}
	}
	return 0, false
}

func modelAt(beta []float64, p Point, d Dip, tau float64, slope bool) float64 {
	v := beta[0] + beta[1]*d.Shape(p.T-d.Mid-tau)
	if slope {
		v += beta[2] * (p.T - d.Mid)
	}
	return v
}

// normal is one filter's normal equations.
type normal struct {
	a   [3][3]float64
	b   [3]float64
	syy float64
}

// chiOf is solveAt's chi-square alone from the normal equations in
// sums, one per filter, without allocating; ok says some filter has points in the dip
// and dip that the filters' mean depth is above 0.
func chiOf(sums []normal, slope bool) (chi float64, ok, dip bool) {
	var sw, sws float64
	for g := range sums {
		beta, vd, c, good := sums[g].solve(slope)
		if !good {
			return 0, false, false
		}
		chi += c
		if !math.IsInf(vd, 1) && vd > 0 {
			sw += 1 / vd
			sws += beta[1] / vd
		}
	}
	return chi, sw > 0, sw > 0 && sws > 0
}

// addAll fills sums with the filters' normal equations, sh holding the
// dip's shape at each point.
func addAll(pts []Point, sh []float64, mid float64, sums []normal, slope bool) {
	k := 2
	if slope {
		k = 3
	}
	for g := range sums {
		sums[g] = normal{}
	}
	for i, p := range pts {
		w := 1 / (p.E * p.E)
		s := &sums[p.G]
		c := [3]float64{1, sh[i], p.T - mid}
		for i := range k {
			s.b[i] += w * c[i] * p.Y
			for j := i; j < k; j++ {
				s.a[i][j] += w * c[i] * c[j]
			}
		}
		s.syy += w * p.Y * p.Y
	}
}

// solve solves one filter's normal level, depth and, with slope, a
// straight line, giving the depth's variance and the chi-square. With
// no point in the dip the depth is held at 0 with an infinite variance.
func (s *normal) solve(slope bool) ([3]float64, float64, float64, bool) {
	var ix [3]int
	m := 0
	for i := range 3 {
		if (i == 1 && !(s.a[1][1] > 0)) || (i == 2 && !slope) {
			continue
		}
		ix[m] = i
		m++
	}
	var a [3][5]float64
	for r := range m {
		for c := range m {
			i, j := ix[r], ix[c]
			a[r][c] = s.a[min(i, j)][max(i, j)]
		}
		a[r][3] = s.b[ix[r]]
		if ix[r] == 1 {
			a[r][4] = 1
		}
	}
	for c := range m {
		p := c
		for r := c + 1; r < m; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if !(math.Abs(a[p][c]) > 1e-300) {
			return [3]float64{}, 0, 0, false
		}
		a[c], a[p] = a[p], a[c]
		for r := c + 1; r < m; r++ {
			f := a[r][c] / a[c][c]
			for j := c; j < 5; j++ {
				a[r][j] -= f * a[c][j]
			}
		}
	}
	var x, e [3]float64
	for i := m - 1; i >= 0; i-- {
		v, u := a[i][3], a[i][4]
		for j := i + 1; j < m; j++ {
			v -= a[i][j] * x[j]
			u -= a[i][j] * e[j]
		}
		x[i], e[i] = v/a[i][i], u/a[i][i]
	}
	var beta [3]float64
	vd := math.Inf(1)
	chi := s.syy
	for r := range m {
		beta[ix[r]] = x[r]
		chi -= x[r] * s.b[ix[r]]
		if ix[r] == 1 {
			vd = e[r]
		}
	}
	return beta, vd, math.Max(chi, 0), true
}

// solveAt solves each filter's normal level, depth and, with slope, a
// straight line for the dip's middle at d.Mid+tau, by the normal
// equations, with each depth's variance and the summed chi-square.
func solveAt(pts []Point, ng int, d Dip, tau float64, slope bool) ([][]float64, []float64, float64, bool) {
	sums := make([]normal, ng)
	sh := make([]float64, len(pts))
	for i, p := range pts {
		sh[i] = d.Shape(p.T - d.Mid - tau)
	}
	addAll(pts, sh, d.Mid, sums, slope)
	betas := make([][]float64, ng)
	vds := make([]float64, ng)
	var chi float64
	for g := range sums {
		beta, vd, c, ok := sums[g].solve(slope)
		if !ok {
			return nil, nil, 0, false
		}
		betas[g], vds[g] = beta[:], vd
		chi += c
	}
	return betas, vds, chi, true
}

// bead is the spread of the fitted middle when the residuals of the best
// fit are moved along the night one place at a time: noise that drifts
// over the night shows here when the formal error misses it. With it
// come the fitted middle less the second method's on each moved night
// the second method times.
func bead(pts []Point, ng int, d Dip, span float64, slope bool, best fitOut) (float64, []float64) {
	n := len(pts)
	shifts := min(n-1, beadShifts)
	taus := make([]float64, 0, shifts)
	var diffs []float64
	shifted := make([]Point, n)
	hit := refit(ng, []Dip{d}, span, slope, best, shifts, beadSteps, func(j int) []Point {
		k := 1 + j*(n-1)/shifts
		for i, p := range pts {
			o := (i + k) % n
			shifted[i] = Point{T: p.T, Y: best.model[i] + best.resid[o]/pts[o].E*p.E, E: p.E, G: p.G}
		}
		return shifted
	}, func(f fitOut, fd Dip, ps []Point) {
		taus = append(taus, f.tau)
		t := fd.Mid + f.tau
		if c, ok := KvW(normalized(ps, f), t, fd.Half); ok {
			diffs = append(diffs, t-c.T)
		}
	})
	if hit {
		return math.Min(span, beadReach*best.formal), diffs
	}
	if len(taus) < 3 {
		return 0, diffs
	}
	return stddev(taus), diffs
}

// redFactor is how much drifting noise widens the error, from the
// residuals in units of their own errors: the larger of the
// time-averaging factor of Pont, Zucker and Queloz (2006) as Winn et al.
// (2008) use it, the scatter of bin means over what white noise would
// give, the largest over bins of 2 points to a fifth of them, and the
// factor sqrt((1+r)/(1-r)) of the residuals' correlation r from one
// point to the next.
func redFactor(pts []Point, raw []float64) float64 {
	n := len(raw)
	resid := make([]float64, n)
	for i := range raw {
		resid[i] = raw[i] / pts[i].E
	}
	s1 := stddev(resid)
	if !(s1 > 0) {
		return 1
	}
	worst := 1.0
	var c0, c1 float64
	for i := range resid {
		c0 += resid[i] * resid[i]
		if i > 0 {
			c1 += resid[i] * resid[i-1]
		}
	}
	if r := c1 / c0; r > 0 && r < 0.95 {
		worst = math.Sqrt((1 + r) / (1 - r))
	} else if r >= 0.95 {
		worst = math.Sqrt(1.95 / 0.05)
	}
	for size := 2; size <= n/5; size++ {
		bins := n / size
		if bins < 3 {
			break
		}
		means := make([]float64, bins)
		for b := range bins {
			var m float64
			for i := b * size; i < (b+1)*size; i++ {
				m += resid[i]
			}
			means[b] = m / float64(size)
		}
		want := s1 / math.Sqrt(float64(size)) * math.Sqrt(float64(bins)/float64(bins-1))
		var ss float64
		for _, m := range means {
			ss += m * m
		}
		worst = math.Max(worst, math.Sqrt(ss/float64(bins))/want)
	}
	return math.Max(1, worst)
}

func stddev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var m float64
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(v)-1))
}

// KvW times the minimum by Kwee and van Woerden (1956): the points within
// half of guess are put on an even grid, and the time about which the two
// sides mirror best is found from a parabola through the summed squared
// differences. Its error is their formula with Z, the number of
// independent points, taken as a quarter of the points inside the
// mirrored range.
func KvW(pts []Point, guess, half float64) (Check, bool) {
	var in []Point
	for _, p := range pts {
		if math.Abs(p.T-guess) < half {
			in = append(in, p)
		}
	}
	slices.SortFunc(in, func(a, b Point) int { return cmpF(a.T, b.T) })
	if len(in) < 12 {
		return Check{}, false
	}
	gaps := make([]float64, 0, len(in)-1)
	for i := 1; i < len(in); i++ {
		if g := in[i].T - in[i-1].T; g > 0 {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) < 3 {
		return Check{}, false
	}
	dt := median(gaps)
	gapMax := 3 * dt
	interp := func(x float64) (float64, bool) {
		j, _ := slices.BinarySearchFunc(in, x, func(p Point, t float64) int { return cmpF(p.T, t) })
		switch {
		case j < len(in) && in[j].T == x:
			return in[j].Y, true
		case j == 0 || j >= len(in):
			return 0, false
		}
		a, b := in[j-1], in[j]
		if b.T-a.T > gapMax {
			return 0, false
		}
		return a.Y + (b.Y-a.Y)*(x-a.T)/(b.T-a.T), true
	}
	reach := 0.3 * half
	first, last := in[0].T, in[len(in)-1].T
	pairs := int(math.Floor(math.Min(guess-reach-first, last-guess-reach) / dt))
	if pairs < 3 {
		return Check{}, false
	}
	s := func(t float64) (float64, bool) {
		var sum float64
		n := 0
		for j := 1; j <= pairs; j++ {
			a, ok1 := interp(t - float64(j)*dt)
			b, ok2 := interp(t + float64(j)*dt)
			if ok1 && ok2 {
				sum += (a - b) * (a - b)
				n++
			}
		}
		if n < 3 {
			return 0, false
		}
		return sum / float64(n) * float64(pairs), true
	}
	step := dt / 2
	steps := int(math.Ceil(reach / step))
	bestT, bestS := 0.0, math.Inf(1)
	for j := -steps; j <= steps; j++ {
		t := guess + float64(j)*step
		if v, ok := s(t); ok && v < bestS {
			bestT, bestS = t, v
		}
	}
	if math.IsInf(bestS, 1) || math.Abs(bestT-guess) >= reach-step {
		return Check{}, false
	}
	sm, ok1 := s(bestT - dt)
	sp, ok2 := s(bestT + dt)
	if !ok1 || !ok2 {
		return Check{}, false
	}
	a := (sm + sp - 2*bestS) / (2 * dt * dt)
	b := (sp - sm) / (2 * dt)
	c := bestS
	if !(a > 0) {
		return Check{}, false
	}
	x0 := -b / (2 * a)
	if math.Abs(x0) > dt {
		return Check{}, false
	}
	used := 0
	for _, p := range in {
		if math.Abs(p.T-bestT) <= float64(pairs)*dt {
			used++
		}
	}
	z := float64(used) / 4
	v := (4*a*c - b*b) / (4 * a * a * (z - 1))
	if !(v > 0) || z <= 1 {
		return Check{}, false
	}
	return Check{T: bestT + x0, Err: math.Sqrt(v), Method: MethodKvW}, true
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
