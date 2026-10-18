package discoveries

import (
	"math"
	"slices"
	"sort"
)

// SensorMap takes a registered sub's pixel back to the camera's sensor:
// sensor x = A·x + B·y + C and sensor y = D·x + E·y + F. A hot pixel stays
// on one sensor pixel while the registration walks it across the sky with
// the night's drift, dithers and field rotation, which links it into a
// track: on Cygnis Loop Panel 2's broadband nights of 2026-10-05 to 07
// every one of the 76 to 125 unknown movers a night was such a pixel.
//
// The registration's own map, fitted to the sub's stars when it was
// registered (FitSensor), is kept with the registered sub (Plane.Sensor).
// A sub registered before that has only its footprint, the edges of the
// area its pixels cover, which gives the shift and small rotation but not
// the registration's scale: on Markarian Chain Panel 6's 2026-02-15 night
// a scale of 0.1% put nine hot pixels 2 to 7 px from where the footprint
// said, and they linked as movers.
type SensorMap struct {
	A, B, C, D, E, F float64
	OK               bool
	// Measured says the map is the registration's or was read off the
	// footprint's edges, not taken as the identity for a sub with no edge
	// in the frame.
	Measured bool
}

// At is the sensor pixel under the registered pixel (x, y).
func (m SensorMap) At(x, y float64) (sx, sy float64) {
	return m.A*x + m.B*y + m.C, m.D*x + m.E*y + m.F
}

// Coefficients are A to F in order, as kept with a registered sub.
func (m SensorMap) Coefficients() []float64 {
	return []float64{m.A, m.B, m.C, m.D, m.E, m.F}
}

// SensorMapFrom is the map of the coefficients A to F; ok is false unless
// there are six finite ones.
func SensorMapFrom(c []float64) (SensorMap, bool) {
	if len(c) != 6 || slices.ContainsFunc(c, func(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }) {
		return SensorMap{}, false
	}
	return SensorMap{A: c[0], B: c[1], C: c[2], D: c[3], E: c[4], F: c[5], OK: true, Measured: true}, true
}

// SensorMapOf is a registered sub's map: the registration's when it was
// kept with the sub, the footprint's otherwise.
func SensorMapOf(p *Plane) SensorMap {
	if p.Sensor != nil {
		return *p.Sensor
	}
	return footprint(p)
}

// rotation is the map of a small rotation θ and shift (u, v), as the
// footprint gives it: sensor x = x + θy − u and sensor y = y − θx − v.
func rotation(theta, u, v float64) SensorMap {
	return SensorMap{A: 1, B: theta, C: -u, D: -theta, E: 1, F: -v, OK: true}
}

// turned is m followed by a half turn of a w by h sensor, as a meridian
// flip without a rotator turns it.
func turned(m SensorMap, w, h int) SensorMap {
	return SensorMap{A: -m.A, B: -m.B, C: float64(w-1) - m.C, D: -m.D, E: -m.E, F: float64(h-1) - m.F, OK: m.OK, Measured: m.Measured}
}

const (
	// sensorFitSigma and sensorFitStars pick the stars the registration's
	// map is fitted to: the brightest 400 over 10 times the noise.
	sensorFitSigma = 10.0
	sensorFitStars = 400
	// sensorVoteStars and sensorVoteBin find the shift: the brightest 80
	// stars of each frame vote for the offsets between them in 4 px bins.
	sensorVoteStars = 80
	sensorVoteBin   = 4.0
	sensorVoteTurn  = 0.001
	sensorVoteTurns = 20
	// sensorPairStars, sensorPairMinLen, sensorPairLen and sensorPairBin
	// find a sensor turned by any angle: pairs of the brightest 40 stars at
	// least 200 px apart, of lengths within 1 px, in 0.004 rad bins.
	sensorPairStars  = 40
	sensorPairMinLen = 200.0
	sensorPairLen    = 1.0
	sensorPairBin    = 0.004
	// SensorFitMin and SensorFitMaxRMS are the fewest matched stars and the
	// largest rms residual, in pixels, of a map fitted to them.
	SensorFitMin    = 20
	SensorFitMaxRMS = 0.5
)

// sensorStars are p's brightest round stars, after subtracting its
// background in place.
func sensorStars(p *Plane) [][2]float64 {
	m := Background(p, 128, nil)
	m.Subtract(p)
	src := Extract(p, m.RMSPlane(), nil, sensorFitSigma, 5)
	src = slices.DeleteFunc(src, func(s Source) bool { return s.Edge || s.B < 0.5*s.A })
	sort.Slice(src, func(i, j int) bool { return src[i].Flux > src[j].Flux })
	if len(src) > sensorFitStars {
		src = src[:sensorFitStars]
	}
	out := make([][2]float64, len(src))
	for i, s := range src {
		out[i] = [2]float64{s.X, s.Y}
	}
	return out
}

// FitSensor fits the map from a registered sub, reg, to the calibrated
// sub it was registered from, cal, on the sensor: the stars of both are
// matched, starting from the footprint's shift and rotation or a half
// turn of it, and an affine map fitted to them, so it holds the
// registration's scale as well. It subtracts both planes' backgrounds in
// place. ok is false when fewer than SensorFitMin stars match or they
// scatter about the map by more than SensorFitMaxRMS px.
func FitSensor(reg, cal *Plane) (m SensorMap, rms float64, n int, ok bool) {
	start := footprint(reg)
	if !start.OK {
		start = rotation(0, 0, 0)
	}
	rs, cs := sensorStars(reg), sensorStars(cal)
	if len(rs) < SensorFitMin || len(cs) < SensorFitMin {
		return SensorMap{}, 0, 0, false
	}
	g := NewGrid(cs, 8)
	cx, cy := float64(reg.W)/2, float64(reg.H)/2
	start = rigid(start, cx, cy)
	starts := []SensorMap{start, turned(start, cal.W, cal.H)}
	if th, ok := turnOfPairs(rs, cs); ok {
		for _, t := range []float64{th, th + math.Pi} {
			c, s := math.Cos(t), math.Sin(t)
			starts = append(starts, SensorMap{A: c, B: -s, D: s, E: c})
		}
	}
	for _, s := range starts {
		fm, frms, fn := matchSensor(shiftByVote(s, rs, cs, cx, cy), rs, cs, g, cx, cy)
		if fn > n {
			m, rms, n = fm, frms, fn
		}
	}
	if n < SensorFitMin || !(rms <= SensorFitMaxRMS) {
		return SensorMap{}, rms, n, false
	}
	m.OK, m.Measured = true, true
	return m, rms, n, true
}

// shiftByVote turns m about the frame's centre (cx, cy) by up to 0.02 rad
// either way, by which a footprint of a sub turned several degrees can be
// off, and moves it by the offset, and keeps the turn, that most of the
// brightest stars agree on.
func shiftByVote(m SensorMap, rs, cs [][2]float64, cx, cy float64) SensorMap {
	best, bestN := m, 0
	for k := -sensorVoteTurns; k <= sensorVoteTurns; k++ {
		t := turnedBy(m, float64(k)*sensorVoteTurn, cx, cy)
		votes := map[[2]int]int{}
		bin, n := [2]int{}, 0
		for _, r := range rs[:min(len(rs), sensorVoteStars)] {
			px, py := t.At(r[0], r[1])
			for _, c := range cs[:min(len(cs), sensorVoteStars)] {
				b := [2]int{int(math.Floor((c[0] - px) / sensorVoteBin)), int(math.Floor((c[1] - py) / sensorVoteBin))}
				votes[b]++
				if votes[b] > n {
					bin, n = b, votes[b]
				}
			}
		}
		if n > bestN {
			t.C += (float64(bin[0]) + 0.5) * sensorVoteBin
			t.F += (float64(bin[1]) + 0.5) * sensorVoteBin
			best, bestN = t, n
		}
	}
	return best
}

// turnOfPairs is the angle the sensor is turned by from the registered
// frame, as when a rotator was moved during the night: the brightest 40
// stars of each frame are joined in pairs, and pairs of the same length
// vote for the difference of their directions, up to a half turn, as a
// pair's stars may rank in either order.
func turnOfPairs(rs, cs [][2]float64) (float64, bool) {
	type seg struct{ l, a float64 }
	segs := func(ps [][2]float64) []seg {
		ps = ps[:min(len(ps), sensorPairStars)]
		var out []seg
		for i := range ps {
			for j := i + 1; j < len(ps); j++ {
				dx, dy := ps[j][0]-ps[i][0], ps[j][1]-ps[i][1]
				if l := math.Hypot(dx, dy); l >= sensorPairMinLen {
					out = append(out, seg{l, math.Atan2(dy, dx)})
				}
			}
		}
		return out
	}
	sr, sc := segs(rs), segs(cs)
	bins := int(math.Ceil(math.Pi / sensorPairBin))
	votes := make([]int, bins)
	for _, r := range sr {
		for _, c := range sc {
			if math.Abs(r.l-c.l) > sensorPairLen {
				continue
			}
			d := math.Mod(c.a-r.a+4*math.Pi, math.Pi)
			votes[int(d/sensorPairBin)%bins]++
		}
	}
	best, n := 0, 0
	for i, v := range votes {
		if v > n {
			best, n = i, v
		}
	}
	return (float64(best) + 0.5) * sensorPairBin, n > 0
}

// rigid is the footprint's first-order rotation as a true one, about the
// point it puts the frame's centre (cx, cy) on: a sub turned 0.09 rad
// otherwise comes out 0.4% too large.
func rigid(m SensorMap, cx, cy float64) SensorMap {
	x0, y0 := m.At(cx, cy)
	th := math.Atan(m.B)
	c, s := math.Cos(th), math.Sin(th)
	return SensorMap{A: c, B: s, C: x0 - c*cx - s*cy, D: -s, E: c, F: y0 + s*cx - c*cy, OK: m.OK, Measured: m.Measured}
}

// turnedBy is m with the registered frame first turned by θ about (cx, cy).
func turnedBy(m SensorMap, theta, cx, cy float64) SensorMap {
	c, s := math.Cos(theta), math.Sin(theta)
	a, b := m.A*c-m.B*s, m.A*s+m.B*c
	d, e := m.D*c-m.E*s, m.D*s+m.E*c
	x0, y0 := m.At(cx, cy)
	out := m
	out.A, out.B, out.D, out.E = a, b, d, e
	out.C, out.F = x0-a*cx-b*cy, y0-d*cx-e*cy
	return out
}

// matchSensor refines m by pairing each registered star with the nearest
// calibrated star to where m puts it, in ever smaller radii, and fitting
// an affine map to the pairs, about (cx, cy) for conditioning. The last
// fit leaves out pairs over 3 sigma from it.
func matchSensor(m SensorMap, rs, cs [][2]float64, g *Grid, cx, cy float64) (SensorMap, float64, int) {
	type pair struct{ x, y, sx, sy float64 }
	var rms float64
	var n int
	for pass, r := range []float64{6, 3, 2, 1.5, 1, 1} {
		var ps []pair
		for _, s := range rs {
			px, py := m.At(s[0], s[1])
			if i, _ := g.Nearest(px, py, r); i >= 0 {
				ps = append(ps, pair{s[0] - cx, s[1] - cy, cs[i][0], cs[i][1]})
			}
		}
		if pass == 5 && len(ps) >= SensorFitMin {
			res := make([]float64, len(ps))
			for i, p := range ps {
				px, py := m.At(p.x+cx, p.y+cy)
				res[i] = math.Hypot(p.sx-px, p.sy-py)
			}
			lim := math.Max(3*1.4826*median64(slices.Clone(res)), 0.3)
			ps = slices.DeleteFunc(ps, func(p pair) bool {
				px, py := m.At(p.x+cx, p.y+cy)
				return math.Hypot(p.sx-px, p.sy-py) > lim
			})
		}
		if len(ps) < SensorFitMin {
			return m, 0, len(ps)
		}
		var ata [3][3]float64
		var bx, by [3]float64
		for _, p := range ps {
			row := [3]float64{1, p.x, p.y}
			for i := range 3 {
				bx[i] += row[i] * p.sx
				by[i] += row[i] * p.sy
				for j := range 3 {
					ata[i][j] += row[i] * row[j]
				}
			}
		}
		sx, ok1 := solve3(ata, bx)
		sy, ok2 := solve3(ata, by)
		if !ok1 || !ok2 {
			return m, 0, 0
		}
		m = SensorMap{A: sx[1], B: sx[2], C: sx[0] - sx[1]*cx - sx[2]*cy, D: sy[1], E: sy[2], F: sy[0] - sy[1]*cx - sy[2]*cy}
		var ss float64
		for _, p := range ps {
			px, py := m.At(p.x+cx, p.y+cy)
			ss += (p.sx-px)*(p.sx-px) + (p.sy-py)*(p.sy-py)
		}
		rms, n = math.Sqrt(ss/float64(len(ps))), len(ps)
	}
	return m, rms, n
}

const (
	footprintStep   = 8
	footprintClip   = 2.0
	footprintPasses = 8
	minFootprint    = 50
)

// footprintEq is one point of a sub's footprint edge: a·(θ, u, v) = rhs.
type footprintEq struct {
	a   [3]float64
	rhs float64
}

// firstLast are the first and last of n indices where covered is true,
// -1 when there is none.
func firstLast(n int, covered func(int) bool) (first, last int) {
	first, last = -1, -1
	for i := range n {
		if covered(i) {
			first = i
			break
		}
	}
	if first < 0 {
		return -1, -1
	}
	for i := n - 1; i >= 0; i-- {
		if covered(i) {
			last = i
			break
		}
	}
	return first, last
}

// footprintEdges are the points where every 8th row and column of a sub
// leave the area its pixels cover, inside the frame.
func footprintEdges(p *Plane) (eqs []footprintEq, rows, cols int) {
	w, h := p.W, p.H
	for y := 0; y < h; y += footprintStep {
		l, r := firstLast(w, func(x int) bool { return !isNaN32(p.Data[y*w+x]) })
		fy := float64(y)
		if l > 0 {
			eqs = append(eqs, footprintEq{[3]float64{fy, -1, 0}, -float64(l)})
			rows++
		}
		if r >= 0 && r < w-1 {
			eqs = append(eqs, footprintEq{[3]float64{fy, -1, 0}, float64(w-1) - float64(r)})
			rows++
		}
	}
	for x := 0; x < w; x += footprintStep {
		t, b := firstLast(h, func(y int) bool { return !isNaN32(p.Data[y*w+x]) })
		fx := float64(x)
		if t > 0 {
			eqs = append(eqs, footprintEq{[3]float64{-fx, 0, -1}, -float64(t)})
			cols++
		}
		if b >= 0 && b < h-1 {
			eqs = append(eqs, footprintEq{[3]float64{-fx, 0, -1}, float64(h-1) - float64(b)})
			cols++
		}
	}
	return eqs, rows, cols
}

// footprint fits a sub's shift and small rotation to the first and last
// covered pixel of every 8th row and column that lie inside the frame,
// leaving out by their residuals the points near a corner, which lie on
// the other edge. An axis with none, because the sub covers it from edge
// to edge, is not shifted along it; a sub that covers the whole frame is
// the reference itself.
func footprint(p *Plane) SensorMap {
	eqs, rows, cols := footprintEdges(p)
	if rows == 0 {
		eqs = append(eqs, footprintEq{[3]float64{0, 1e3, 0}, 0})
	}
	if cols == 0 {
		eqs = append(eqs, footprintEq{[3]float64{0, 0, 1e3}, 0})
	}
	if rows+cols == 0 {
		return rotation(0, 0, 0)
	}
	if rows+cols < minFootprint {
		return SensorMap{}
	}
	use := make([]bool, len(eqs))
	for i := range use {
		use[i] = true
	}
	var sol [3]float64
	for range footprintPasses {
		var ata [3][3]float64
		var atb [3]float64
		for i, e := range eqs {
			if !use[i] {
				continue
			}
			for r := range 3 {
				atb[r] += e.a[r] * e.rhs
				for c := range 3 {
					ata[r][c] += e.a[r] * e.a[c]
				}
			}
		}
		s, ok := solve3(ata, atb)
		if !ok {
			return SensorMap{}
		}
		sol = s
		res := make([]float64, len(eqs))
		var kept []float64
		for i, e := range eqs {
			res[i] = math.Abs(e.a[0]*sol[0] + e.a[1]*sol[1] + e.a[2]*sol[2] - e.rhs)
			if use[i] {
				kept = append(kept, res[i])
			}
		}
		clip := math.Max(footprintClip, 3*1.4826*median64(kept))
		for i := range eqs {
			use[i] = res[i] <= clip
		}
	}
	m := rotation(sol[0], sol[1], sol[2])
	m.Measured = true
	return m
}
