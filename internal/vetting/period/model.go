package period

import (
	"math"
)

const clipSigma = 5.0

type flatPts struct {
	t, y, e []float64
	g, s    []int
	ng, ns  int
	tref    float64
	keep    []bool
}

func flatten(d *data) *flatPts {
	f := &flatPts{ng: len(d.groups), ns: len(d.series), tref: math.Round(d.t0)}
	for _, se := range d.series {
		for _, p := range se.fit {
			f.t = append(f.t, p.t-f.tref)
			f.y = append(f.y, p.y)
			f.e = append(f.e, fitErr(p.e))
			f.g = append(f.g, se.group)
			f.s = append(f.s, se.idx)
		}
	}
	f.keep = make([]bool, len(f.t))
	for i := range f.keep {
		f.keep[i] = true
	}
	return f
}

type dipP struct{ phase, width, shape float64 }

type ecFit struct {
	P, T0   float64
	dips    []dipP
	depth   [][]float64
	active  [][]bool
	offset  []float64
	res     lmResult
	keep    []bool
	iPh     []int
	iW, iA  []int
	iD      [][]int
	iC      []int
	robust  float64
	nParams int
	timing  bool
}

func profile(u, w, a float64) (float64, float64, float64, float64) {
	if math.Abs(u) >= w {
		return 0, 0, 0, 0
	}
	r := u / w
	q := 1 - r*r
	if q < 1e-9 {
		q = 1e-9
	}
	qa := math.Pow(q, a)
	qa1 := a * qa / q
	return qa, qa1 * (-2 * u / (w * w)), qa1 * (2 * u * u / (w * w * w)), qa * math.Log(q)
}

func shapeOnly(u, w, a float64) float64 {
	if math.Abs(u) >= w {
		return 0
	}
	r := u / w
	return math.Pow(math.Max(1-r*r, 1e-9), a)
}

func (m *ecFit) layout(ng, ns int) int {
	nd := len(m.dips)
	n := 2
	m.iPh, m.iW, m.iA = make([]int, nd), make([]int, nd), make([]int, nd)
	for k := range nd {
		m.iPh[k] = -1
		if k > 0 {
			m.iPh[k] = n
			n++
		}
		m.iW[k], m.iA[k] = n, n+1
		n += 2
	}
	m.iD = make([][]int, ng)
	for g := range ng {
		m.iD[g] = make([]int, nd)
		for k := range nd {
			m.iD[g][k] = n
			n++
		}
	}
	m.iC = make([]int, ns)
	for s := range ns {
		m.iC[s] = n
		n++
	}
	return n
}

func (m *ecFit) pack() []float64 {
	p := make([]float64, m.nParams)
	p[0], p[1] = m.P, m.T0
	for k, dp := range m.dips {
		if k > 0 {
			p[m.iPh[k]] = dp.phase
		}
		p[m.iW[k]], p[m.iA[k]] = dp.width, dp.shape
	}
	for g := range m.iD {
		for k := range m.dips {
			p[m.iD[g][k]] = m.depth[g][k]
		}
	}
	for s := range m.iC {
		p[m.iC[s]] = m.offset[s]
	}
	return p
}

func (m *ecFit) unpack(p []float64) {
	m.P, m.T0 = p[0], p[1]
	for k := range m.dips {
		if k > 0 {
			m.dips[k].phase = p[m.iPh[k]]
		}
		m.dips[k].width, m.dips[k].shape = p[m.iW[k]], p[m.iA[k]]
	}
	for g := range m.iD {
		for k := range m.dips {
			m.depth[g][k] = p[m.iD[g][k]]
		}
	}
	for s := range m.iC {
		m.offset[s] = p[m.iC[s]]
	}
}

func (m *ecFit) evalFn(f *flatPts) evalFn {
	return func(i int, p []float64, idx []int, der []float64) (float64, int) {
		P, T0 := p[0], p[1]
		dt := f.t[i] - T0
		x := dt / P
		g, s := f.g[i], f.s[i]
		val := p[m.iC[s]]
		idx[0], der[0] = 0, 0
		idx[1], der[1] = 1, 0
		n := 2
		for k := range m.dips {
			ph := 0.0
			if k > 0 {
				ph = p[m.iPh[k]]
			}
			u := wrapPhase(x - ph)
			sh, dsdu, dsdw, dsda := profile(u, p[m.iW[k]], p[m.iA[k]])
			dd := p[m.iD[g][k]]
			val += dd * sh
			der[0] += dd * dsdu * (-dt / (P * P))
			der[1] += dd * dsdu * (-1 / P)
			if k > 0 {
				idx[n], der[n] = m.iPh[k], -dd*dsdu
				n++
			}
			idx[n], der[n] = m.iW[k], dd*dsdw
			idx[n+1], der[n+1] = m.iA[k], dd*dsda
			idx[n+2], der[n+2] = m.iD[g][k], sh
			n += 3
		}
		idx[n], der[n] = m.iC[s], 1
		n++
		return val, n
	}
}

func (m *ecFit) value(t float64, g, s int) float64 {
	x := (t - m.T0) / m.P
	val := 0.0
	if s >= 0 {
		val = m.offset[s]
	}
	for k, dp := range m.dips {
		sh := shapeOnly(wrapPhase(x-dp.phase), dp.width, dp.shape)
		val += m.depth[g][k] * sh
	}
	return val
}

func (m *ecFit) shapeAt(t float64, k int) (float64, float64) {
	dp := m.dips[k]
	u := wrapPhase((t-m.T0)/m.P - dp.phase)
	sh := shapeOnly(u, dp.width, dp.shape)
	return sh, u
}

type ecInit struct {
	P, T0 float64
	dips  []dipP
	freeP bool
	fixed bool
	mask  []bool
}

func newEcFit(f *flatPts, d *data, in ecInit) *ecFit {
	m := &ecFit{P: in.P, T0: in.T0, dips: append([]dipP(nil), in.dips...)}
	m.nParams = m.layout(f.ng, f.ns)
	m.depth = make([][]float64, f.ng)
	m.active = make([][]bool, f.ng)
	for g := range m.depth {
		m.depth[g] = make([]float64, len(m.dips))
		m.active[g] = make([]bool, len(m.dips))
	}
	m.offset = make([]float64, f.ns)
	m.keep = append([]bool(nil), f.keep...)
	if in.mask != nil {
		copy(m.keep, in.mask)
	}
	m.initDepths(f, d)
	return m
}

func (m *ecFit) initDepths(f *flatPts, d *data) {
	for k := range m.dips {
		sum := make([]float64, f.ng)
		wt := make([]float64, f.ng)
		for i := range f.t {
			if !m.keep[i] {
				continue
			}
			sh, _ := m.shapeAt(f.t[i], k)
			if sh > 0.5 {
				w := 1 / (f.e[i] * f.e[i])
				sum[f.g[i]] += w * f.y[i] / sh
				wt[f.g[i]] += w
			}
		}
		for g := range f.ng {
			if wt[g] > 0 {
				m.depth[g][k] = math.Max(sum[g]/wt[g], 0.01)
			} else {
				m.depth[g][k] = 0.05
			}
		}
	}
	m.updateActive(f, d)
}

func (m *ecFit) updateActive(f *flatPts, d *data) {
	cov := make([][]int, f.ng)
	for g := range cov {
		cov[g] = make([]int, len(m.dips))
	}
	for i := range f.t {
		if !m.keep[i] {
			continue
		}
		for k := range m.dips {
			if sh, _ := m.shapeAt(f.t[i], k); sh > 0.25 {
				cov[f.g[i]][k]++
			}
		}
	}
	for k := range m.dips {
		var sum, n float64
		for g := range f.ng {
			m.active[g][k] = cov[g][k] >= 3
			if m.active[g][k] {
				sum += undilute(m.depth[g][k], d.groups[g].dil)
				n++
			}
		}
		if n == 0 {
			continue
		}
		for g := range f.ng {
			if !m.active[g][k] {
				m.depth[g][k] = dilute(sum/n, d.groups[g].dil)
			}
		}
	}
}

func (m *ecFit) bounds(in ecInit, live []bool) ([]float64, []float64, []bool) {
	lo := make([]float64, m.nParams)
	hi := make([]float64, m.nParams)
	on := make([]bool, m.nParams)
	dp := 1e-3 * in.P
	free := !in.fixed
	lo[0], hi[0], on[0] = in.P-dp, in.P+dp, in.freeP && free
	lo[1], hi[1], on[1] = in.T0-0.25*in.P, in.T0+0.25*in.P, free
	for k, d := range in.dips {
		if k > 0 {
			lo[m.iPh[k]], hi[m.iPh[k]], on[m.iPh[k]] = d.phase-0.15, d.phase+0.15, free
		}
		lo[m.iW[k]], hi[m.iW[k]], on[m.iW[k]] = 0.0015, 0.3, free
		lo[m.iA[k]], hi[m.iA[k]], on[m.iA[k]] = 0.3, 8, free
	}
	for g := range m.iD {
		for k := range m.dips {
			i := m.iD[g][k]
			lo[i], hi[i], on[i] = 0, 4, m.active[g][k]
		}
	}
	for s := range m.iC {
		i := m.iC[s]
		lo[i], hi[i], on[i] = -2, 2, live[s]
	}
	return lo, hi, on
}

func fitEclipse(f *flatPts, d *data, in ecInit) *ecFit {
	m := newEcFit(f, d, in)
	live := make([]bool, f.ns)
	for i, k := range m.keep {
		live[f.s[i]] = live[f.s[i]] || k
	}
	for pass := range 4 {
		lo, hi, on := m.bounds(in, live)
		pr := &lmProblem{y: f.y, e: f.e, keep: m.keep, lo: lo, hi: hi, on: on, eval: m.evalFn(f), maxNNZ: 3 + 4*len(m.dips)}
		m.res = pr.solve(m.pack())
		m.unpack(m.res.p)
		if pass == 3 {
			break
		}
		changed := false
		for i := range f.t {
			r := (f.y[i] - m.value(f.t[i], f.g[i], f.s[i])) / f.e[i]
			k := math.Abs(r) < clipSigma && (in.mask == nil || in.mask[i])
			changed = changed || k != m.keep[i]
			m.keep[i] = k
		}
		if !changed {
			break
		}
		m.updateActive(f, d)
	}
	m.robust = robustScore(f, func(i int) float64 { return m.value(f.t[i], f.g[i], f.s[i]) })
	return m
}

func robustScore(f *flatPts, model func(i int) float64) float64 {
	var c float64
	for i := range f.t {
		r := (f.y[i] - model(i)) / f.e[i]
		c += math.Min(r*r, clipSigma*clipSigma)
	}
	return c
}

func (m *ecFit) freeCount() int {
	n := 0
	for _, a := range m.active {
		for _, b := range a {
			if b {
				n++
			}
		}
	}
	return n + 2 + 3*len(m.dips) - 1 + len(m.offset)
}

func (m *ecFit) depthSigma(g, k int) float64 {
	if !m.active[g][k] {
		return math.Inf(1)
	}
	return m.res.sigma(m.iD[g][k])
}

func flatFit(f *flatPts) ([]float64, float64) {
	off := make([]float64, f.ns)
	keep := append([]bool(nil), f.keep...)
	for range 3 {
		sum := make([]float64, f.ns)
		wt := make([]float64, f.ns)
		for i := range f.t {
			if keep[i] {
				w := 1 / (f.e[i] * f.e[i])
				sum[f.s[i]] += w * f.y[i]
				wt[f.s[i]] += w
			}
		}
		for s := range off {
			if wt[s] > 0 {
				off[s] = sum[s] / wt[s]
			}
		}
		for i := range f.t {
			keep[i] = math.Abs(f.y[i]-off[f.s[i]])/f.e[i] < clipSigma
		}
	}
	return off, robustScore(f, func(i int) float64 { return off[f.s[i]] })
}
