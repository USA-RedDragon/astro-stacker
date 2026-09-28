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
	// RejectGrow also rejects pixels within this many pixels of a rejected
	// cluster. A satellite trail's faint edges are below the threshold pixel
	// by pixel but sit right beside its rejected core.
	RejectGrow int
	// LocalNorm matches each sub's sky, gradients and transparency to the
	// reference before folding it in, instead of subtracting one
	// background level, when the master's coverage is uneven (see fitSky
	// and needsLocalNorm).
	LocalNorm bool
}

var DefaultOptions = Options{SaturationLevel: 0.9, RejectSigma: 4, MinSamples: 8, RejectGrow: 2, LocalNorm: true}

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
	sky := skyFor(sub, a.W, a.H, exposure, a, opts)
	norm := sky.normalizer(exposure)
	k2 := opts.RejectSigma * opts.RejectSigma
	// Pixels are independent, so judging all of them before folding any in
	// gives the same result as judging each just before it's folded.
	reject := make([]bool, len(sub))
	for i, v := range sub {
		if v == 0 || v >= opts.SaturationLevel {
			continue
		}
		wOld := a.Weight[i]
		if a.Count[i] >= opts.MinSamples && wOld > 0 {
			delta := norm.at(i, v) - a.Mean[i]
			reject[i] = delta*delta > k2*(a.M2[i]/wOld)
		}
	}
	return a.fold(sub, exposure, weight, sky, reject, opts), nil
}

// skyFor is a sub's sky: fitted against ref with LocalNorm, otherwise its
// background level.
func skyFor(sub []float32, w, h int, exposure float64, ref *Accumulator, opts Options) skyModel {
	flat := background(sub, opts.SaturationLevel) / exposure
	if !opts.LocalNorm || !needsLocalNorm(ref) {
		return flatSky(w, h, flat)
	}
	return fitSky(func(i int) float32 { return sub[i] }, w, h, exposure, ref, opts.SaturationLevel, flat)
}

// fold adds a sub's pixels that aren't empty, saturated or rejected (after
// growing the rejection by opts.RejectGrow), normalized by its sky. A sub
// scaled up for haze is scaled noise too, so its weight drops by the square.
func (a *Accumulator) fold(sub []float32, exposure, weight float64, sky skyModel, reject []bool, opts Options) AddResult {
	weight *= sky.scale * sky.scale
	res := AddResult{Background: sky.mean / sky.scale}
	reject = grow(reject, a.W, a.H, opts.RejectGrow)
	norm := sky.normalizer(exposure)
	w := float32(weight)
	for i, v := range sub {
		switch {
		case v == 0:
			// Registration leaves exact zeros outside the sub's footprint.
			res.Empty++
			continue
		case v >= opts.SaturationLevel:
			res.Saturated++
			continue
		case reject[i]:
			res.Rejected++
			continue
		}
		// Per second, with this sub's sky removed so moonlit and dark subs
		// agree on the background and aren't rejected against each other.
		x := norm.at(i, v)
		wOld := a.Weight[i]
		wNew := wOld + w
		delta := x - a.Mean[i]
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
	return res
}

// grow widens rejected clusters (pixels with at least two rejected
// neighbours, as along a trail) to squares of radius r. Lone rejections,
// mostly noise, stay as they are.
func grow(mask []bool, w, h, r int) []bool {
	if r <= 0 {
		return mask
	}
	seeds := make([]bool, len(mask))
	for y := range h {
		for x := range w {
			if !mask[y*w+x] {
				continue
			}
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if (dx != 0 || dy != 0) && xx >= 0 && xx < w && yy >= 0 && yy < h && mask[yy*w+xx] {
						n++
					}
				}
			}
			seeds[y*w+x] = n >= 2
		}
	}
	// Separable: rows, then columns, each with a running count.
	tmp := make([]bool, len(mask))
	for y := range h {
		row := seeds[y*w : (y+1)*w]
		n := 0
		for x := range min(r, w) {
			if row[x] {
				n++
			}
		}
		for x := range w {
			if x+r < w && row[x+r] {
				n++
			}
			if x-r-1 >= 0 && row[x-r-1] {
				n--
			}
			tmp[y*w+x] = n > 0
		}
	}
	out := make([]bool, len(mask))
	for x := range w {
		n := 0
		for y := range min(r, h) {
			if tmp[y*w+x] {
				n++
			}
		}
		for y := range h {
			if y+r < h && tmp[(y+r)*w+x] {
				n++
			}
			if y-r-1 >= 0 && tmp[(y-r-1)*w+x] {
				n--
			}
			out[y*w+x] = n > 0 || mask[y*w+x]
		}
	}
	return out
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
	fillSaturated(out, a.Weight, a.W, a.H)
	return out
}

// fillSaturated sets pixels without data that data surrounds to 1, full
// scale. They are star and galaxy cores saturated in every sub, so every
// sample was left out; written as 0 they show as black or, when only one
// filter saturates, coloured dots. Pixels without data that reach the
// image's edge are registration borders and stay 0.
func fillSaturated(out, weight []float32, w, h int) {
	border := make([]bool, w*h)
	var queue []int
	visit := func(i int) {
		if weight[i] == 0 && !border[i] {
			border[i] = true
			queue = append(queue, i)
		}
	}
	for x := range w {
		visit(x)
		visit((h-1)*w + x)
	}
	for y := range h {
		visit(y * w)
		visit(y*w + w - 1)
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > 0 {
			visit(i - 1)
		}
		if x < w-1 {
			visit(i + 1)
		}
		if y > 0 {
			visit(i - w)
		}
		if y < h-1 {
			visit(i + w)
		}
	}
	for i, wt := range weight {
		if wt == 0 && !border[i] {
			out[i] = 1
		}
	}
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
