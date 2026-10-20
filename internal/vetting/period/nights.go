package period

import (
	"math"
	"sort"
)

const (
	// nightMinPts is the fewest points out of the dips a night needs to
	// count toward its band's night-to-night offset.
	nightMinPts = 3
	// nightMinNights is the fewest such nights a band needs for its
	// night-to-night offset to be measured; with fewer it is taken as 0.
	nightMinNights = 5
)

// nightKey is one night of one zero point.
type nightKey struct {
	s int
	n int64
}

// nightAcc sums one night's points out of the dips.
type nightAcc struct{ sw, swy, n float64 }

// nightOffsets is, per group, the scatter of the nightly mean level out
// of the dips beyond the points' own errors: a night's zero point, sky,
// flat field or neighbour residual moves every point of that night
// together, which a dip measured on that night alone cannot tell from its
// depth. It is the robust spread of the nightly weighted means, less in
// quadrature the median nightly mean error, from nights of nightMinPts
// points or more; 0 for a group with fewer than nightMinNights.
func nightOffsets(ng int, tref float64, each func(fn func(g, s int, t, y, e float64))) []float64 {
	acc := make([]map[nightKey]*nightAcc, ng)
	each(func(g, s int, t, y, e float64) {
		if acc[g] == nil {
			acc[g] = map[nightKey]*nightAcc{}
		}
		k := nightKey{s, nightOf(t + tref)}
		x := acc[g][k]
		if x == nil {
			x = &nightAcc{}
			acc[g][k] = x
		}
		w := 1 / (e * e)
		x.sw += w
		x.swy += w * y
		x.n++
	})
	out := make([]float64, ng)
	for g, nights := range acc {
		var means, errs []float64
		for _, x := range nights {
			if x.n < nightMinPts || x.sw == 0 {
				continue
			}
			means = append(means, x.swy/x.sw)
			errs = append(errs, 1/math.Sqrt(x.sw))
		}
		if len(means) < nightMinNights {
			continue
		}
		sd := mad(means, median(means))
		se := median(errs)
		out[g] = math.Sqrt(math.Max(0, sd*sd-se*se))
	}
	return out
}

// fitNightOffsets is nightOffsets of the points fit m keeps out of all
// its dips, against m's zero points.
func (a *analysis) fitNightOffsets(m *ecFit) []float64 {
	f := a.f
	return nightOffsets(f.ng, f.tref, func(fn func(g, s int, t, y, e float64)) {
		for i := range f.t {
			if !m.keep[i] || m.inAnyDip(f.t[i]) {
				continue
			}
			fn(f.g[i], f.s[i], f.t[i], f.y[i]-m.offset[f.s[i]], f.e[i])
		}
	})
}

// inAnyDip says time t falls in one of m's dips.
func (m *ecFit) inAnyDip(t float64) bool {
	for k := range m.dips {
		if sh, _ := m.shapeAt(t, k); sh > 0 {
			return true
		}
	}
	return false
}

// dipNights is how many nights of group g fit m keeps a point of in dip k
// at half depth or more.
func (a *analysis) dipNights(m *ecFit, g, k int) int {
	f := a.f
	seen := map[nightKey]bool{}
	for i := range f.t {
		if !m.keep[i] || f.g[i] != g {
			continue
		}
		if sh, _ := m.shapeAt(f.t[i], k); sh >= 0.5 {
			seen[nightKey{f.s[i], nightOf(f.t[i] + f.tref)}] = true
		}
	}
	return len(seen)
}

// depthSigmaNights is fit m's error on dip k's depth in group g with the
// night-to-night offset of the nights that caught the dip added.
func (a *analysis) depthSigmaNights(m *ecFit, g, k int) float64 {
	s := m.depthSigma(g, k)
	sn := a.fitNightOffsets(m)[g]
	if sn == 0 || math.IsInf(s, 1) {
		return s
	}
	n := a.dipNights(m, g, k)
	if n == 0 {
		return s
	}
	return math.Hypot(s, sn/math.Sqrt(float64(n)))
}

// sectorParityZ is how many sigma a TESS sector's mean depth must stand
// above zero for its odd and even depths to be taken as shares of it.
const sectorParityZ = 3.0

// sectorParity compares odd and even cycles within each TESS sector that
// caught both: each sector's even and odd depths (combine) are taken as
// shares of that sector's mean depth, the shares averaged over sectors by
// their errors, and the result put back on the scale of the sectors'
// mean depth. It reports false when no sector caught both parities with a
// clear dip.
func sectorParity[T any](even, odd []T, combine func([]T) (float64, float64, bool), sector func(T) int) ([2]float64, [2]float64, bool) {
	bySector := func(es []T) map[int][]T {
		m := map[int][]T{}
		for _, x := range es {
			m[sector(x)] = append(m[sector(x)], x)
		}
		return m
	}
	ev, od := bySector(even), bySector(odd)
	var sw, swy [2]float64
	var nw, nwy float64
	for sec, es := range ev {
		os, ok := od[sec]
		if !ok {
			continue
		}
		me, ee, ok1 := combine(es)
		mo, eo, ok2 := combine(os)
		if !ok1 || !ok2 {
			continue
		}
		we, wo := 1/(ee*ee), 1/(eo*eo)
		n := (we*me + wo*mo) / (we + wo)
		ne := 1 / math.Sqrt(we+wo)
		if n <= 0 || n < sectorParityZ*ne {
			continue
		}
		for p, v := range [][2]float64{{me / n, ee / n}, {mo / n, eo / n}} {
			w := 1 / (v[1] * v[1])
			sw[p] += w
			swy[p] += w * v[0]
		}
		nw += 1 / (ne * ne)
		nwy += n / (ne * ne)
	}
	if nw == 0 {
		return [2]float64{}, [2]float64{}, false
	}
	scale := nwy / nw
	var s, e [2]float64
	for p := range 2 {
		s[p] = swy[p] / sw[p] * scale
		e[p] = scale / math.Sqrt(sw[p])
	}
	return s, e, true
}

const (
	// levelHalfSpan is the half width, in days, of the window of points
	// out of the dips that sets a series' running normal level.
	levelHalfSpan = 15.0
	// levelMinPts is the fewest points out of the dips that window needs.
	levelMinPts = 8
)

// runningLevel is each series' normal level out of the dips over time:
// a survey's zero point drifts from season to season and template to
// template, more than one level per series allows.
type runningLevel struct {
	t, y map[int][]float64
}

// newRunningLevel takes the points out of the dips as series, time and
// level.
func newRunningLevel(each func(fn func(s int, t, y float64))) *runningLevel {
	type ty struct{ t, y float64 }
	by := map[int][]ty{}
	each(func(s int, t, y float64) { by[s] = append(by[s], ty{t, y}) })
	r := &runningLevel{t: map[int][]float64{}, y: map[int][]float64{}}
	for s, v := range by {
		sort.Slice(v, func(i, j int) bool { return v[i].t < v[j].t })
		for _, p := range v {
			r.t[s] = append(r.t[s], p.t)
			r.y[s] = append(r.y[s], p.y)
		}
	}
	return r
}

// at is series s's median level within levelHalfSpan days of t, when it
// has levelMinPts points there.
func (r *runningLevel) at(s int, t float64) (float64, bool) {
	ts := r.t[s]
	lo := sort.SearchFloat64s(ts, t-levelHalfSpan)
	hi := sort.SearchFloat64s(ts, t+levelHalfSpan)
	if hi-lo < levelMinPts {
		return 0, false
	}
	return median(r.y[s][lo:hi]), true
}
