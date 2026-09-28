// Package stacking builds one master per target and filter, adding each good
// sub as it arrives instead of re-stacking everything.
package stacking

import (
	"fmt"
	"math"
	"slices"
)

// Options tune how a sub is folded into the master.
type Options struct {
	// SaturationLevel is the calibrated value (0-1 scale) at or above which a
	// pixel is treated as saturated and left out, so shorter subs fill in
	// clipped cores.
	SaturationLevel float32
	// RejectSigma rejects a pixel more than this many standard deviations
	// from the running mean, once MinSamples values are in.
	RejectSigma float32
	MinSamples  float32
}

var DefaultOptions = Options{SaturationLevel: 0.9, RejectSigma: 4, MinSamples: 8}

// Accumulator holds the running state for one master: per pixel, the total
// weight, the weighted mean and sum of squared deviations (West's weighted
// incremental algorithm), and how many subs contributed.
type Accumulator struct {
	W, H   int
	Weight []float32
	Mean   []float32
	M2     []float32
	Count  []float32

	// Background tracks the weighted mean sky level (per second) of the
	// subs, which is added back when writing the master.
	BackgroundSum float64
	WeightSum     float64
	Subs          int
}

func NewAccumulator(w, h int) *Accumulator {
	n := w * h
	return &Accumulator{W: w, H: h, Weight: make([]float32, n), Mean: make([]float32, n), M2: make([]float32, n), Count: make([]float32, n)}
}

// AddResult reports what happened to one sub's pixels.
type AddResult struct {
	Used, Rejected, Saturated, Empty int
	Background                       float64 // per second
}

// Add folds a registered, calibrated sub into the master. sub is on Siril's
// 0-1 scale; exposure is in seconds; weight is the sub's quality weight
// (score × exposure).
func (a *Accumulator) Add(sub []float32, exposure, weight float64, opts Options) (AddResult, error) {
	if len(sub) != a.W*a.H {
		return AddResult{}, fmt.Errorf("sub has %d pixels, master %d", len(sub), a.W*a.H)
	}
	if !(exposure > 0) || !(weight > 0) {
		return AddResult{}, fmt.Errorf("exposure %v and weight %v must be positive", exposure, weight)
	}
	bg := background(sub, opts.SaturationLevel)
	res := AddResult{Background: bg / exposure}
	inv := float32(1 / exposure)
	w := float32(weight)
	k2 := opts.RejectSigma * opts.RejectSigma

	for i, v := range sub {
		switch {
		case v == 0:
			// Registration leaves exact zeros outside the sub's footprint.
			res.Empty++
			continue
		case v >= opts.SaturationLevel:
			res.Saturated++
			continue
		}
		// Per second, with this sub's sky removed so moonlit and dark subs
		// agree on the background and aren't rejected against each other.
		x := (v - float32(bg)) * inv
		wOld := a.Weight[i]
		delta := x - a.Mean[i]
		if a.Count[i] >= opts.MinSamples && wOld > 0 {
			variance := a.M2[i] / wOld
			if delta*delta > k2*variance {
				res.Rejected++
				continue
			}
		}
		wNew := wOld + w
		r := delta * w / wNew
		a.Mean[i] += r
		a.M2[i] += wOld * delta * r
		a.Weight[i] = wNew
		a.Count[i]++
		res.Used++
	}
	a.BackgroundSum += weight * res.Background
	a.WeightSum += weight
	a.Subs++
	return res, nil
}

// Master returns the stacked image on the 0-1 scale of a single sub of
// scaleExposure seconds, with the average sky added back. Pixels no sub
// covered are 0.
func (a *Accumulator) Master(scaleExposure float64) []float32 {
	out := make([]float32, a.W*a.H)
	if a.WeightSum == 0 {
		return out
	}
	sky := float32(a.BackgroundSum / a.WeightSum)
	s := float32(scaleExposure)
	for i := range out {
		if a.Weight[i] > 0 {
			out[i] = (a.Mean[i] + sky) * s
		}
	}
	return out
}

// background is the median of the pixels that aren't empty or saturated,
// from a strided sample. Calibrated sky can dip below zero, so only exact
// zeros (registration borders) count as empty.
func background(p []float32, saturation float32) float64 {
	stride := max(1, len(p)/400_000)
	s := make([]float32, 0, len(p)/stride+1)
	for i := 0; i < len(p); i += stride {
		if v := p[i]; v != 0 && v < saturation {
			s = append(s, v)
		}
	}
	if len(s) == 0 {
		return 0
	}
	slices.Sort(s)
	return float64(s[len(s)/2])
}

// Planes returns the state as four planes for storage: weight, mean, M2 and
// count, in that order.
func (a *Accumulator) Planes() []float32 {
	n := a.W * a.H
	out := make([]float32, 0, 4*n)
	out = append(out, a.Weight...)
	out = append(out, a.Mean...)
	out = append(out, a.M2...)
	return append(out, a.Count...)
}

// FromPlanes restores an accumulator saved with Planes.
func FromPlanes(w, h int, planes []float32, backgroundSum, weightSum float64, subs int) (*Accumulator, error) {
	n := w * h
	if len(planes) != 4*n {
		return nil, fmt.Errorf("state has %d samples, want %d", len(planes), 4*n)
	}
	if math.IsNaN(backgroundSum) || math.IsNaN(weightSum) {
		return nil, fmt.Errorf("state totals are NaN")
	}
	return &Accumulator{
		W: w, H: h,
		Weight: planes[:n:n], Mean: planes[n : 2*n : 2*n], M2: planes[2*n : 3*n : 3*n], Count: planes[3*n:],
		BackgroundSum: backgroundSum, WeightSum: weightSum, Subs: subs,
	}, nil
}
