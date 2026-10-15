package period

import (
	"math"
)

type sineFit struct {
	P, T0  float64
	amp    []float64
	offset []float64
	res    lmResult
	keep   []bool
	robust float64
	ng     int
}

func (m *sineFit) value(t float64, g, s int) float64 {
	v := m.amp[g] * math.Cos(2*math.Pi*(t-m.T0)/m.P)
	if s >= 0 {
		v += m.offset[s]
	}
	return v
}

func fitSine(f *flatPts, period float64, freeP bool) *sineFit {
	m := &sineFit{P: period, ng: f.ng, amp: make([]float64, f.ng), offset: make([]float64, f.ns), keep: append([]bool(nil), f.keep...)}
	var sc, ss float64
	for i := range f.t {
		w := 1 / (f.e[i] * f.e[i])
		a := 2 * math.Pi * f.t[i] / period
		sc += w * f.y[i] * math.Cos(a)
		ss += w * f.y[i] * math.Sin(a)
	}
	m.T0 = period * math.Atan2(ss, sc) / (2 * math.Pi)
	for g := range m.amp {
		m.amp[g] = 0.05
	}
	np := 2 + f.ng + f.ns
	lo, hi, on := make([]float64, np), make([]float64, np), make([]bool, np)
	lo[0], hi[0], on[0] = period*(1-1e-3), period*(1+1e-3), freeP
	lo[1], hi[1], on[1] = m.T0-0.5*period, m.T0+0.5*period, true
	for g := range f.ng {
		lo[2+g], hi[2+g], on[2+g] = 0, 4, true
	}
	for s := range f.ns {
		lo[2+f.ng+s], hi[2+f.ng+s], on[2+f.ng+s] = -2, 2, true
	}
	eval := func(i int, p []float64, idx []int, der []float64) (float64, int) {
		dt := f.t[i] - p[1]
		th := 2 * math.Pi * dt / p[0]
		c, s := math.Cos(th), math.Sin(th)
		a := p[2+f.g[i]]
		idx[0], der[0] = 0, a*s*2*math.Pi*dt/(p[0]*p[0])
		idx[1], der[1] = 1, a*s*2*math.Pi/p[0]
		idx[2], der[2] = 2+f.g[i], c
		idx[3], der[3] = 2+f.ng+f.s[i], 1
		return a*c + p[2+f.ng+f.s[i]], 4
	}
	p := make([]float64, np)
	p[0], p[1] = m.P, m.T0
	for g := range f.ng {
		p[2+g] = m.amp[g]
	}
	for pass := range 4 {
		pr := &lmProblem{y: f.y, e: f.e, keep: m.keep, lo: lo, hi: hi, on: on, eval: eval, maxNNZ: 4}
		m.res = pr.solve(p)
		p = m.res.p
		m.P, m.T0 = p[0], p[1]
		copy(m.amp, p[2:2+f.ng])
		copy(m.offset, p[2+f.ng:])
		if pass == 3 {
			break
		}
		for i := range f.t {
			m.keep[i] = math.Abs(f.y[i]-m.value(f.t[i], f.g[i], f.s[i]))/f.e[i] < clipSigma
		}
	}
	m.robust = robustScore(f, func(i int) float64 { return m.value(f.t[i], f.g[i], f.s[i]) })
	return m
}

func (m *sineFit) freeCount() int { return 2 + len(m.amp) + len(m.offset) }

func (m *sineFit) ampZ() float64 {
	best := 0.0
	for g := range m.amp {
		if s := m.res.sigma(2 + g); s > 0 && !math.IsInf(s, 0) {
			best = math.Max(best, m.amp[g]/s)
		}
	}
	return best
}
