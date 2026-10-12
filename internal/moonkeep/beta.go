package moonkeep

import "math"

func betacf(a, b, x float64) float64 {
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
		if math.Abs(del-1) < 1e-12 {
			break
		}
	}
	return h
}

// BetaCDF is the regularized incomplete beta function I_x(a, b).
func BetaCDF(x, a, b float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	la, _ := math.Lgamma(a + b)
	lb, _ := math.Lgamma(a)
	lc, _ := math.Lgamma(b)
	front := math.Exp(la - lb - lc + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		return front * betacf(a, b, x) / a
	}
	return 1 - front*betacf(b, a, 1-x)/b
}

// BetaQuantile is the x with BetaCDF(x, a, b) = p.
func BetaQuantile(p, a, b float64) float64 {
	if !(a > 0 && b > 0) || math.IsNaN(p) {
		return math.NaN()
	}
	lo, hi := 0.0, 1.0
	for range 100 {
		mid := (lo + hi) / 2
		if BetaCDF(mid, a, b) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}
