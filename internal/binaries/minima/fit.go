// Package minima times eclipse minima from one night's photometry and
// reads the trends in a star's timings against its ephemeris.
package minima

import (
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
	Slope  bool    `json:"slope"`
	Formal float64 `json:"formal"`
	Bead   float64 `json:"bead"`
	// Red is how much more the residuals' averages over a few frames
	// scatter than white noise would; the formal error is widened by it.
	Red float64 `json:"red"`
	// Check is the second method's time, when the points allow it, and
	// Agree whether it lands within three of the combined errors.
	Check *Check `json:"check,omitempty"`
	Agree bool   `json:"agree"`
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
	// beadMin is the fewest points to move the residuals along, beadShifts
	// the most moves made, each refitted over beadReach formal errors
	// either side of the best middle in beadSteps.
	beadMin    = 8
	beadShifts = 60
	beadReach  = 10.0
	beadSteps  = 200
	// agreeSigma is how many combined errors the two methods may differ.
	agreeSigma = 3.0
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
)

type fitOut struct {
	tau, formal  float64
	depth, dErr  float64
	chi          float64
	k            int
	n            int
	betas        [][]float64
	model, resid []float64
}

// Time fits the minimum of d in pts: one middle for every filter, each
// filter (G) with its own normal level and depth. ok is false with a
// reason in plain words when the points do not pin a minimum down.
func Time(pts []Point, d Dip) (Minimum, string, bool) {
	if d.Half <= 0 || d.Shape == nil {
		return Minimum{}, ReasonNoFit, false
	}
	span := searchShare * d.Half
	reach := windowShare*d.Half + span
	byGroup := map[int][]Point{}
	for _, p := range pts {
		if math.Abs(p.T-d.Mid) <= reach && finite(p.T) && finite(p.Y) {
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
	if len(use) < minInDip+2 {
		return Minimum{}, ReasonFew, false
	}
	flat, ok := fitShape(use, ng, d, span, false)
	if !ok {
		return Minimum{}, ReasonNoFit, false
	}
	best := flat
	slope := false
	if len(use) >= slopeMin {
		if line, ok := fitShape(use, ng, d, span, true); ok && line.n-line.k > 0 {
			f := (flat.chi - line.chi) / float64(line.k-flat.k) / (line.chi / float64(line.n-line.k))
			if FProb(f, float64(line.k-flat.k), float64(line.n-line.k)) < slopeP {
				best, slope = line, true
			}
		}
	}
	m := Minimum{T: d.Mid + best.tau, Method: MethodModel, Points: best.n, Depth: best.depth, DepthErr: best.dErr, Slope: slope, Formal: best.formal}
	for _, p := range use {
		if d.Shape(p.T-m.T) <= inDip {
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
		return m, ReasonSides, false
	case m.Before+m.After < minInDip:
		return m, ReasonNoBranch, false
	case !(m.Depth > minDepthZ*m.DepthErr):
		return m, ReasonShallow, false
	case math.Abs(best.tau) > 0.95*span:
		return m, ReasonEdge, false
	case !(best.formal > 0) || best.formal >= span:
		return m, ReasonLoose, false
	}
	if len(use) >= beadMin {
		m.Bead = bead(use, ng, d, span, slope, best)
		m.Red = redFactor(best.resid)
	}
	m.Err = math.Max(m.Formal*math.Max(1, m.Red), m.Bead)
	if c, ok := KvW(normalized(use, best, d, slope), m.T, d.Half); ok {
		m.Check = &c
		m.Agree = math.Abs(c.T-m.T) <= agreeSigma*math.Hypot(c.Err, m.Err)
	} else {
		m.Agree = true
	}
	return m, "", true
}

// normalized puts every filter's points on one dip of depth 1 over a
// flat normal level, by the best fit, for the second method.
func normalized(pts []Point, f fitOut, d Dip, slope bool) []Point {
	out := make([]Point, len(pts))
	for i, p := range pts {
		b := f.betas[p.G]
		y := p.Y - b[0]
		if slope {
			y -= b[2] * (p.T - d.Mid)
		}
		out[i] = Point{T: p.T, Y: y / b[1], E: p.E / b[1]}
	}
	return out
}

// fitShape steps the dip's middle over ±span about d.Mid, solving each
// of the ng filters' normal level, depth and, with slope, a straight
// line at each step, and takes the best. Its error is where chi-square
// rises by the reduced chi-square, at least 1, either side.
func fitShape(pts []Point, ng int, d Dip, span float64, slope bool) (fitOut, bool) {
	return fitShapeIn(pts, ng, d, -span, span, gridSteps, slope)
}

// fitShapeIn is fitShape over lo to hi in steps.
func fitShapeIn(pts []Point, ng int, d Dip, lo, hi float64, steps int, slope bool) (fitOut, bool) {
	per := 2
	if slope {
		per = 3
	}
	k := per * ng
	n := len(pts)
	if n <= k || hi <= lo || ng == 0 {
		return fitOut{}, false
	}
	span := (hi - lo) / 2
	h := (hi - lo) / float64(steps)
	chis := make([]float64, steps+1)
	sums := make([]normal, ng)
	bi := -1
	for j := range chis {
		tau := lo + float64(j)*h
		chi, ok := chiAt(pts, sums, d, tau, slope)
		if !ok {
			chis[j] = math.Inf(1)
			continue
		}
		chis[j] = chi
		if bi < 0 || chi < chis[bi] {
			bi = j
		}
	}
	if bi < 0 {
		return fitOut{}, false
	}
	tau := lo + float64(bi)*h
	if bi > 0 && bi < steps {
		c0, c1, c2 := chis[bi-1], chis[bi], chis[bi+1]
		if den := c0 - 2*c1 + c2; den > 0 && finite(den) {
			tau += h * 0.5 * (c0 - c2) / den
		}
	}
	betas, covs, chi, ok := solveAt(pts, ng, d, tau, slope)
	if !ok {
		return fitOut{}, false
	}
	red := math.Max(1, chi/float64(n-k))
	var sw, sws float64
	for g := range ng {
		if !(betas[g][1] > 0) {
			return fitOut{}, false
		}
		w := 1 / (covs[g][1][1] * red)
		sw += w
		sws += w * betas[g][1]
	}
	lim := chi + red
	cl, okLo := crossing(chis, bi, -1, lim)
	ch, okHi := crossing(chis, bi, 1, lim)
	formal := span
	if okLo && okHi {
		formal = (ch - cl) * h / 2
	}
	out := fitOut{tau: tau, formal: formal, depth: sws / sw, dErr: 1 / math.Sqrt(sw), chi: chi, k: k, n: n, betas: betas}
	out.model = make([]float64, n)
	out.resid = make([]float64, n)
	for i, p := range pts {
		out.model[i] = modelAt(betas[p.G], p, d, tau, slope)
		out.resid[i] = p.Y - out.model[i]
	}
	return out, true
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

// chiAt is solveAt's chi-square alone, solved in place in sums, one per
// filter, without allocating.
func chiAt(pts []Point, sums []normal, d Dip, tau float64, slope bool) (float64, bool) {
	k := 2
	if slope {
		k = 3
	}
	for g := range sums {
		sums[g] = normal{}
	}
	for _, p := range pts {
		w := 1 / (p.E * p.E)
		s := &sums[p.G]
		c := [3]float64{1, d.Shape(p.T - d.Mid - tau), p.T - d.Mid}
		for i := range k {
			s.b[i] += w * c[i] * p.Y
			for j := i; j < k; j++ {
				s.a[i][j] += w * c[i] * c[j]
			}
		}
		s.syy += w * p.Y * p.Y
	}
	var chi float64
	for g := range sums {
		s := &sums[g]
		var m [3][4]float64
		for i := range k {
			for j := range k {
				m[i][j] = s.a[min(i, j)][max(i, j)]
			}
			m[i][3] = s.b[i]
		}
		for c := range k {
			p := c
			for r := c + 1; r < k; r++ {
				if math.Abs(m[r][c]) > math.Abs(m[p][c]) {
					p = r
				}
			}
			if !(math.Abs(m[p][c]) > 1e-300) {
				return 0, false
			}
			m[c], m[p] = m[p], m[c]
			for r := c + 1; r < k; r++ {
				f := m[r][c] / m[c][c]
				for j := c; j < 4; j++ {
					m[r][j] -= f * m[c][j]
				}
			}
		}
		var beta [3]float64
		for i := k - 1; i >= 0; i-- {
			v := m[i][3]
			for j := i + 1; j < k; j++ {
				v -= m[i][j] * beta[j]
			}
			beta[i] = v / m[i][i]
		}
		x := s.syy
		for i := range k {
			x -= beta[i] * s.b[i]
		}
		chi += math.Max(x, 0)
	}
	return chi, true
}

// solveAt solves each filter's normal level, depth and, with slope, a
// straight line for the dip's middle at d.Mid+tau, by the normal
// equations, and sums their chi-squares.
func solveAt(pts []Point, ng int, d Dip, tau float64, slope bool) ([][]float64, [][][]float64, float64, bool) {
	k := 2
	if slope {
		k = 3
	}
	a := make([][3][3]float64, ng)
	b := make([][3]float64, ng)
	syy := make([]float64, ng)
	var c [3]float64
	for _, p := range pts {
		w := 1 / (p.E * p.E)
		g := p.G
		c[0], c[1], c[2] = 1, d.Shape(p.T-d.Mid-tau), p.T-d.Mid
		for i := range k {
			b[g][i] += w * c[i] * p.Y
			for j := i; j < k; j++ {
				a[g][i][j] += w * c[i] * c[j]
			}
		}
		syy[g] += w * p.Y * p.Y
	}
	betas := make([][]float64, ng)
	covs := make([][][]float64, ng)
	var chi float64
	for g := range ng {
		m := make([][]float64, k)
		for i := range k {
			m[i] = make([]float64, k)
			for j := range k {
				m[i][j] = a[g][min(i, j)][max(i, j)]
			}
		}
		cov, ok := invert(m)
		if !ok {
			return nil, nil, 0, false
		}
		beta := make([]float64, k)
		x := syy[g]
		for i := range k {
			for j := range k {
				beta[i] += cov[i][j] * b[g][j]
			}
			x -= beta[i] * b[g][i]
		}
		betas[g], covs[g] = beta, cov
		chi += math.Max(x, 0)
	}
	return betas, covs, chi, true
}

// bead is the spread of the fitted middle when the residuals of the best
// fit are moved along the night one place at a time: noise that drifts
// over the night shows here when the formal error misses it.
func bead(pts []Point, ng int, d Dip, span float64, slope bool, best fitOut) float64 {
	n := len(pts)
	shifts := min(n-1, beadShifts)
	taus := make([]float64, 0, shifts)
	shifted := make([]Point, n)
	reach := math.Min(span, beadReach*best.formal)
	lo, hi := math.Max(-span, best.tau-reach), math.Min(span, best.tau+reach)
	for j := range shifts {
		k := 1 + j*(n-1)/shifts
		for i, p := range pts {
			shifted[i] = Point{T: p.T, Y: best.model[i] + best.resid[(i+k)%n], E: p.E, G: p.G}
		}
		f, ok := fitShapeIn(shifted, ng, d, lo, hi, beadSteps, slope)
		if !ok {
			continue
		}
		if edge := (hi - lo) / beadSteps; f.tau <= lo+edge || f.tau >= hi-edge {
			return reach
		}
		taus = append(taus, f.tau)
	}
	if len(taus) < 3 {
		return 0
	}
	return stddev(taus)
}

// redFactor is the time-averaging factor of Pont, Zucker and Queloz
// (2006) as Winn et al. (2008) use it: for bins of 2 points up to a
// fifth of them, the scatter of the bin means over what white noise
// would give, the largest over the bin sizes; 1 for white noise.
func redFactor(resid []float64) float64 {
	n := len(resid)
	s1 := stddev(resid)
	if !(s1 > 0) {
		return 1
	}
	worst := 1.0
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
// independent points, taken as a quarter of the points used.
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
	z := float64(len(in)) / 4
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
