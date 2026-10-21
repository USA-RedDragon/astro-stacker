package minima

import "math"

// LSQ solves rows·beta = y by weighted least squares with one sigma
// errors s: the fitted beta, its covariance and the chi-square.
func LSQ(rows [][]float64, y, s []float64) ([]float64, [][]float64, float64, bool) {
	if len(rows) == 0 {
		return nil, nil, 0, false
	}
	k := len(rows[0])
	a := make([][]float64, k)
	for i := range a {
		a[i] = make([]float64, k)
	}
	b := make([]float64, k)
	for i, r := range rows {
		w := 1 / (s[i] * s[i])
		for p := range k {
			b[p] += w * r[p] * y[i]
			for q := range k {
				a[p][q] += w * r[p] * r[q]
			}
		}
	}
	cov, ok := invert(a)
	if !ok {
		return nil, nil, 0, false
	}
	beta, chi := lsqWith(rows, cov, b, y, s)
	return beta, cov, chi, true
}

// lsqWith is LSQ's coefficients and chi-square from the inverse normal
// matrix cov the rows and errors s give and the right-hand side b; with
// b nil it is worked out from y.
func lsqWith(rows, cov [][]float64, b, y, s []float64) ([]float64, float64) {
	k := len(cov)
	if b == nil {
		b = make([]float64, k)
		for i, r := range rows {
			w := 1 / (s[i] * s[i])
			for p := range k {
				b[p] += w * r[p] * y[i]
			}
		}
	}
	beta := make([]float64, k)
	for p := range k {
		for q := range k {
			beta[p] += cov[p][q] * b[q]
		}
	}
	var chi float64
	for i, r := range rows {
		var m float64
		for p := range k {
			m += r[p] * beta[p]
		}
		z := (y[i] - m) / s[i]
		chi += z * z
	}
	return beta, chi
}

// invert inverts a symmetric positive matrix by Gauss-Jordan with
// partial pivoting, scaled to its diagonal first.
func invert(a [][]float64) ([][]float64, bool) {
	n := len(a)
	d := make([]float64, n)
	for i := range n {
		if !(a[i][i] > 0) {
			return nil, false
		}
		d[i] = 1 / math.Sqrt(a[i][i])
	}
	m := make([][]float64, n)
	for i := range n {
		m[i] = make([]float64, 2*n)
		for j := range n {
			m[i][j] = a[i][j] * d[i] * d[j]
		}
		m[i][n+i] = 1
	}
	for c := range n {
		p := c
		for r := c + 1; r < n; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[p][c]) {
				p = r
			}
		}
		if math.Abs(m[p][c]) < 1e-12 {
			return nil, false
		}
		m[c], m[p] = m[p], m[c]
		piv := m[c][c]
		for j := range 2 * n {
			m[c][j] /= piv
		}
		for r := range n {
			if r == c || m[r][c] == 0 {
				continue
			}
			f := m[r][c]
			for j := range 2 * n {
				m[r][j] -= f * m[c][j]
			}
		}
	}
	out := make([][]float64, n)
	for i := range n {
		out[i] = make([]float64, n)
		for j := range n {
			out[i][j] = m[i][n+j] * d[i] * d[j]
		}
	}
	return out, true
}

// FProb is the chance that an F statistic with d1 and d2 degrees of
// freedom reaches f by noise alone.
func FProb(f, d1, d2 float64) float64 {
	if !(f > 0) || d1 <= 0 || d2 <= 0 {
		return 1
	}
	return betaReg(d2/2, d1/2, d2/(d2+d1*f))
}

// ChiProb is the chance that a chi-square with k degrees of freedom
// reaches x by noise alone.
func ChiProb(x, k float64) float64 {
	if !(x > 0) || k <= 0 {
		return 1
	}
	return 1 - gammaReg(k/2, x/2)
}

// betaReg is the regularized incomplete beta function I_x(a, b).
func betaReg(a, b, x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	front := math.Exp(lab - la - lb + a*math.Log(x) + b*math.Log(1-x))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(a, b, x) / a
	}
	return 1 - front*betaCF(b, a, 1-x)/b
}

func betaCF(a, b, x float64) float64 {
	const tiny = 1e-300
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= 300; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-14 {
			break
		}
	}
	return h
}

// gammaReg is the regularized lower incomplete gamma function P(a, x).
func gammaReg(a, x float64) float64 {
	if x <= 0 {
		return 0
	}
	lg, _ := math.Lgamma(a)
	if x < a+1 {
		sum, del, ap := 1/a, 1/a, a
		for range 500 {
			ap++
			del *= x / ap
			sum += del
			if math.Abs(del) < math.Abs(sum)*1e-15 {
				break
			}
		}
		return sum * math.Exp(-x+a*math.Log(x)-lg)
	}
	const tiny = 1e-300
	b := x + 1 - a
	c := 1 / tiny
	d := 1 / b
	h := d
	for i := 1; i <= 500; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-15 {
			break
		}
	}
	return 1 - math.Exp(-x+a*math.Log(x)-lg)*h
}

// rng is a small deterministic generator, so the same timings always give
// the same chance levels.
type rng struct{ s uint64 }

func (r *rng) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *rng) uniform() float64 { return (float64(r.next()>>11) + 0.5) / (1 << 53) }

// normal is a standard normal draw by Box and Muller.
func (r *rng) normal() float64 {
	return math.Sqrt(-2*math.Log(r.uniform())) * math.Cos(2*math.Pi*r.uniform())
}
