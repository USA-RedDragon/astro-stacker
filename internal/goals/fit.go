package goals

import (
	"math"
	"slices"
)

type DrawPoint struct {
	N     int     `json:"n"`
	Hours float64 `json:"t"`
	Sigma float64 `json:"sigma"`
}

func fitCost(pts []DrawPoint, la, b float64) float64 {
	a2 := math.Exp(2 * la)
	var c float64
	for _, p := range pts {
		d := 0.5*math.Log(a2/p.Hours+b*b) - math.Log(p.Sigma)
		c += d * d
	}
	return c
}

func golden(f func(float64) float64, lo, hi float64, iters int) (float64, float64) {
	const g = 0.6180339887498949
	x1 := hi - g*(hi-lo)
	x2 := lo + g*(hi-lo)
	f1, f2 := f(x1), f(x2)
	for range iters {
		if f1 <= f2 {
			hi, x2, f2 = x2, x1, f1
			x1 = hi - g*(hi-lo)
			f1 = f(x1)
		} else {
			lo, x1, f1 = x1, x2, f2
			x2 = lo + g*(hi-lo)
			f2 = f(x2)
		}
	}
	if f1 <= f2 {
		return x1, f1
	}
	return x2, f2
}

func usable(pts []DrawPoint) []DrawPoint {
	out := make([]DrawPoint, 0, len(pts))
	for _, p := range pts {
		if p.Hours > 0 && p.Sigma > 0 && !math.IsNaN(p.Sigma) && !math.IsInf(p.Sigma, 0) {
			out = append(out, p)
		}
	}
	return out
}

func bestA(pts []DrawPoint, b, la0 float64) (float64, float64) {
	return golden(func(la float64) float64 { return fitCost(pts, la, b) }, la0-6, la0+6, 80)
}

func FitNoise(points []DrawPoint) (a, b float64, ok bool) {
	pts := usable(points)
	if len(pts) < 2 {
		return 0, 0, false
	}
	var la0 float64
	minS := math.Inf(1)
	for _, p := range pts {
		la0 += math.Log(p.Sigma) + 0.5*math.Log(p.Hours)
		minS = math.Min(minS, p.Sigma)
	}
	la0 /= float64(len(pts))
	const steps = 200
	bestB, bestLA := 0.0, la0
	bestC := fitCost(pts, la0, 0)
	for k := range steps {
		bb := minS * float64(k) / steps
		la, c := bestA(pts, bb, la0)
		if c < bestC {
			bestB, bestLA, bestC = bb, la, c
		}
	}
	lo := math.Max(0, bestB-minS/steps)
	hi := math.Min(minS, bestB+minS/steps)
	if hi > lo {
		bb, c := golden(func(bb float64) float64 {
			_, c := bestA(pts, bb, la0)
			return c
		}, lo, hi, 40)
		if c < bestC {
			la, _ := bestA(pts, bb, la0)
			bestB, bestLA = bb, la
		}
	}
	return math.Exp(bestLA), bestB, true
}

func heldOut(points []DrawPoint, n int) (float64, bool) {
	pts := usable(points)
	if len(pts) == 0 {
		return 0, false
	}
	top := 0
	for _, p := range pts {
		top = max(top, p.N)
	}
	var train []DrawPoint
	var topT, topS []float64
	levels := map[int]bool{}
	for _, p := range pts {
		switch {
		case p.N == top:
			topT = append(topT, p.Hours)
			topS = append(topS, p.Sigma)
		case p.N <= n/4:
			train = append(train, p)
			levels[p.N] = true
		}
	}
	if len(levels) < 3 || len(topS) == 0 {
		return 0, false
	}
	a, b, ok := FitNoise(train)
	if !ok {
		return 0, false
	}
	slices.Sort(topT)
	pred := NoiseAt(a, b, percentileSorted(topT, 50))
	meas := median(topS)
	if meas <= 0 {
		return 0, false
	}
	return 100 * (pred/meas - 1), true
}
