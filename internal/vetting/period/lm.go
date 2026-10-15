package period

import (
	"math"
)

type evalFn func(i int, p []float64, idx []int, der []float64) (float64, int)

type lmProblem struct {
	y, e   []float64
	keep   []bool
	lo, hi []float64
	on     []bool
	eval   evalFn
	maxNNZ int
}

type lmResult struct {
	p    []float64
	cov  [][]float64
	chi2 float64
	dof  int
	n    int
}

func (pr *lmProblem) chi2(p []float64) float64 {
	const chunk = 2048
	nc := (len(pr.y) + chunk - 1) / chunk
	parts := make([]float64, nc)
	parallelChunks(nc, func(c int) {
		idx := make([]int, pr.maxNNZ)
		der := make([]float64, pr.maxNNZ)
		var s float64
		for i := c * chunk; i < min((c+1)*chunk, len(pr.y)); i++ {
			if !pr.keep[i] {
				continue
			}
			m, _ := pr.eval(i, p, idx, der)
			r := (pr.y[i] - m) / pr.e[i]
			s += r * r
		}
		parts[c] = s
	})
	var total float64
	for _, v := range parts {
		total += v
	}
	return total
}

func (pr *lmProblem) normal(p []float64, act []int, pos []int) ([][]float64, []float64, float64, int) {
	na := len(act)
	const chunk = 2048
	nc := (len(pr.y) + chunk - 1) / chunk
	parts := make([]struct {
		a    []float64
		g    []float64
		chi2 float64
		n    int
	}, nc)
	parallelChunks(nc, func(c int) {
		a := make([]float64, na*na)
		g := make([]float64, na)
		idx := make([]int, pr.maxNNZ)
		der := make([]float64, pr.maxNNZ)
		ai := make([]int, pr.maxNNZ)
		var chi2 float64
		n := 0
		for i := c * chunk; i < min((c+1)*chunk, len(pr.y)); i++ {
			if !pr.keep[i] {
				continue
			}
			n++
			m, nnz := pr.eval(i, p, idx, der)
			w := 1 / pr.e[i]
			r := (m - pr.y[i]) * w
			chi2 += r * r
			k := 0
			for j := range nnz {
				if q := pos[idx[j]]; q >= 0 {
					ai[k] = q
					der[k] = der[j] * w
					k++
				}
			}
			for x := range k {
				g[ai[x]] += der[x] * r
				row := a[ai[x]*na:]
				for z := range k {
					row[ai[z]] += der[x] * der[z]
				}
			}
		}
		parts[c].a, parts[c].g, parts[c].chi2, parts[c].n = a, g, chi2, n
	})
	a := make([][]float64, na)
	for i := range a {
		a[i] = make([]float64, na)
	}
	g := make([]float64, na)
	var chi2 float64
	n := 0
	for _, pt := range parts {
		for i := range na {
			g[i] += pt.g[i]
			for j := range na {
				a[i][j] += pt.a[i*na+j]
			}
		}
		chi2 += pt.chi2
		n += pt.n
	}
	return a, g, chi2, n
}

func cholSolve(a [][]float64, b []float64) ([]float64, bool) {
	n := len(a)
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, n)
	}
	for i := range n {
		for j := 0; j <= i; j++ {
			s := a[i][j]
			for k := range j {
				s -= l[i][k] * l[j][k]
			}
			if i == j {
				if s <= 0 || math.IsNaN(s) {
					return nil, false
				}
				l[i][i] = math.Sqrt(s)
			} else {
				l[i][j] = s / l[j][j]
			}
		}
	}
	x := make([]float64, n)
	for i := range n {
		s := b[i]
		for k := range i {
			s -= l[i][k] * x[k]
		}
		x[i] = s / l[i][i]
	}
	for i := n - 1; i >= 0; i-- {
		s := x[i]
		for k := i + 1; k < n; k++ {
			s -= l[k][i] * x[k]
		}
		x[i] = s / l[i][i]
	}
	return x, true
}

func invert(a [][]float64) ([][]float64, bool) {
	n := len(a)
	out := make([][]float64, n)
	for i := range out {
		out[i] = make([]float64, n)
	}
	for j := range n {
		e := make([]float64, n)
		e[j] = 1
		x, ok := cholSolve(a, e)
		if !ok {
			return nil, false
		}
		for i := range n {
			out[i][j] = x[i]
		}
	}
	return out, true
}

func clampParams(p, lo, hi []float64) {
	for i := range p {
		p[i] = math.Min(math.Max(p[i], lo[i]), hi[i])
	}
}

func (pr *lmProblem) solve(p0 []float64) lmResult {
	p := append([]float64(nil), p0...)
	clampParams(p, pr.lo, pr.hi)
	var act []int
	pos := make([]int, len(p))
	for i := range p {
		pos[i] = -1
		if pr.on[i] {
			pos[i] = len(act)
			act = append(act, i)
		}
	}
	lambda := 1e-3
	for range 200 {
		a, g, chi2, _ := pr.normal(p, act, pos)
		improved := false
		for lambda < 1e12 {
			m := make([][]float64, len(act))
			for i := range m {
				m[i] = append([]float64(nil), a[i]...)
				m[i][i] += lambda*a[i][i] + 1e-12
			}
			neg := make([]float64, len(g))
			for i := range g {
				neg[i] = -g[i]
			}
			step, ok := cholSolve(m, neg)
			if !ok {
				lambda *= 10
				continue
			}
			trial := append([]float64(nil), p...)
			for i, q := range act {
				trial[q] += step[i]
			}
			clampParams(trial, pr.lo, pr.hi)
			c := pr.chi2(trial)
			if c < chi2 {
				rel := (chi2 - c) / math.Max(chi2, 1e-300)
				p = trial
				lambda = math.Max(lambda/3, 1e-9)
				improved = rel > 1e-8
				break
			}
			lambda *= 4
		}
		if !improved {
			break
		}
	}
	a, _, chi2, n := pr.normal(p, act, pos)
	dof := n - len(act)
	res := lmResult{p: p, chi2: chi2, dof: dof, n: n}
	res.cov = make([][]float64, len(p))
	for i := range res.cov {
		res.cov[i] = make([]float64, len(p))
	}
	if dof <= 0 {
		return res
	}
	for i := range a {
		a[i][i] += 1e-12
	}
	inv, ok := invert(a)
	if !ok {
		return res
	}
	s := chi2 / float64(dof)
	for i, qi := range act {
		for j, qj := range act {
			res.cov[qi][qj] = inv[i][j] * s
		}
	}
	return res
}

func (r lmResult) sigma(i int) float64 {
	v := r.cov[i][i]
	if v <= 0 {
		return math.Inf(1)
	}
	return math.Sqrt(v)
}
