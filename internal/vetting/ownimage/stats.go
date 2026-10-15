package ownimage

import (
	"math"
	"slices"
)

func isNaN32(v float32) bool { return v != v }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	c := slices.Clone(v)
	slices.Sort(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func robustStd(v []float64) float64 {
	if len(v) < 2 {
		return math.NaN()
	}
	m := median(v)
	d := make([]float64, len(v))
	for i, x := range v {
		d[i] = math.Abs(x - m)
	}
	return 1.4826 * median(d)
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func std(v []float64) float64 {
	if len(v) < 2 {
		return math.NaN()
	}
	m := mean(v)
	var s float64
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)-1))
}

// clippedMean is the mean of v after 3-sigma clipping about the median
// with a robust spread.
func clippedMean(v []float64) (m, sd float64, n int) {
	if len(v) == 0 {
		return math.NaN(), math.NaN(), 0
	}
	med, rs := median(v), robustStd(v)
	keep := make([]float64, 0, len(v))
	for _, x := range v {
		if !(rs > 0) || math.Abs(x-med) <= 3*rs {
			keep = append(keep, x)
		}
	}
	return mean(keep), std(keep), len(keep)
}
