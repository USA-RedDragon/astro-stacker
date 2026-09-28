package stacking

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/USA-RedDragon/pixinsight-worker/internal/store/models/app"
)

// RelativeTolerance widens rejection on bright pixels, where seeing changes
// between subs move a star's peak far more than the noise does.
const RelativeTolerance = 0.15

// AddAgainst folds a sub in like Add, but rejects pixels against fixed
// statistics from an earlier pass (refMean, refStd per pixel, per second)
// instead of the running ones. A pixel with no reference is kept.
func (a *Accumulator) AddAgainst(sub []float32, exposure, weight float64, refMean, refStd []float32, noise float64, opts Options) (AddResult, error) {
	if len(sub) != a.W*a.H || len(refMean) != len(sub) || len(refStd) != len(sub) {
		return AddResult{}, fmt.Errorf("sub, reference and master sizes differ")
	}
	bg := background(sub, opts.SaturationLevel)
	res := AddResult{Background: bg / exposure}
	inv := float32(1 / exposure)
	w := float32(weight)
	k := opts.RejectSigma
	floor := float32(noise / exposure)
	for i, v := range sub {
		switch {
		case v == 0:
			res.Empty++
			continue
		case v >= opts.SaturationLevel:
			res.Saturated++
			continue
		}
		x := (v - float32(bg)) * inv
		if s := refStd[i]; s > 0 {
			tol := k*max(s, floor) + RelativeTolerance*abs32(refMean[i])
			if abs32(x-refMean[i]) > tol {
				res.Rejected++
				continue
			}
		}
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
	return res, nil
}

// Stats returns the per-pixel mean and standard deviation, for rejecting
// against in the next pass.
func (a *Accumulator) Stats() (mean, std []float32) {
	std = make([]float32, len(a.Mean))
	for i, w := range a.Weight {
		if w > 0 && a.Count[i] > 1 {
			std[i] = float32(math.Sqrt(float64(max(a.M2[i], 0) / w)))
		}
	}
	return slices.Clone(a.Mean), std
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// noiseLevel is the background noise of a sub (σ from the MAD of a strided
// sample), on the 0-1 scale.
func noiseLevel(p []float32, saturation float32) float64 {
	med := background(p, saturation)
	stride := max(1, len(p)/400_000)
	s := make([]float32, 0, len(p)/stride+1)
	for i := 0; i < len(p); i += stride {
		if v := p[i]; v != 0 && v < saturation {
			s = append(s, abs32(v-float32(med)))
		}
	}
	if len(s) == 0 {
		return 0
	}
	slices.Sort(s)
	return float64(s[len(s)/2]) * madToSigma
}

const madToSigma = 1.4826

type storedSub struct {
	key      string
	exposure float64
	weight   float64
}

// rebuild recomputes a master from every stored registered sub with proper
// rejection, replacing whatever the incremental updates accumulated.
func (p *Pipeline) rebuild(ctx context.Context, stack *app.Stack) (*Accumulator, error) {
	var rows []app.StackFrame
	if err := p.db.WithContext(ctx).Where("stack_id = ? AND status = ? AND registered_key IS NOT NULL", stack.ID, app.StackStatusAdded).
		Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	subs := make([]storedSub, 0, len(rows))
	for _, r := range rows {
		subs = append(subs, storedSub{key: *r.RegisteredKey, exposure: r.Exposure, weight: r.Weight})
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("no registered subs")
	}
	dir, err := os.MkdirTemp(p.workDir, "rebuild-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	if len(subs) < WarmUpSubs {
		return p.rebuildMedian(ctx, dir, subs)
	}
	passes := 2
	if len(subs) < 40 {
		// With few subs a trail still pulls the first-pass mean and σ, so
		// clip once more against cleaner statistics.
		passes = 3
	}
	return p.rebuildStreaming(ctx, dir, subs, passes)
}

// memOffset lets memSub store slightly negative calibrated values: samples
// are stored as round((v+memOffset)/(1+memOffset)·65535), and 0 means empty.
const memOffset = 0.02

func quantize(v float32) uint16 {
	if v == 0 {
		return 0
	}
	q := math.Round(float64(v+memOffset) / (1 + memOffset) * 65535)
	return uint16(min(max(q, 1), 65535))
}

func dequantize(q uint16) float32 {
	return float32(q)/65535*(1+memOffset) - memOffset
}

// memSub is one sub held in memory for a median-anchored rebuild.
type memSub struct {
	px       []uint16 // quantize()d samples, 0 = empty
	bg       float32  // background per second
	noise    float32  // background noise per second
	exposure float64
	weight   float64
}

// rebuildMedian holds every sub in memory as 16-bit samples and anchors
// rejection on the per-pixel median, which a single outlier can't move.
func (p *Pipeline) rebuildMedian(ctx context.Context, dir string, subs []storedSub) (*Accumulator, error) {
	var all []memSub
	var w, h int
	sat := p.opts.Stack.SaturationLevel
	for i, s := range subs {
		local := filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, local); err != nil {
			return nil, err
		}
		sub, sw, sh, err := readSub(local)
		os.Remove(local)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			w, h = sw, sh
		} else if sw != w || sh != h {
			return nil, fmt.Errorf("sub %s is %dx%d, expected %dx%d", s.key, sw, sh, w, h)
		}
		all = append(all, toMemSub(sub, s.exposure, s.weight, sat))
	}
	return medianAnchored(all, w, h, p.opts.Stack), nil
}

func toMemSub(sub []float32, exposure, weight float64, sat float32) memSub {
	px := make([]uint16, len(sub))
	for j, v := range sub {
		px[j] = quantize(v)
	}
	return memSub{
		px:       px,
		bg:       float32(background(sub, sat) / exposure),
		noise:    float32(noiseLevel(sub, sat) / exposure),
		exposure: exposure,
		weight:   weight,
	}
}

// medianAnchored stacks in-memory subs, rejecting per pixel any value further
// from the median than the sub's noise allows. With fewer than three values a
// pixel keeps them all, since one sub's outlier can't be told from signal.
func medianAnchored(all []memSub, w, h int, opts Options) *Accumulator {
	acc := NewAccumulator(w, h)
	k := opts.RejectSigma
	vals := make([]float32, 0, len(all))
	idx := make([]int, 0, len(all))
	sorted := make([]float32, 0, len(all))
	for i := range w * h {
		vals, idx = vals[:0], idx[:0]
		for j, l := range all {
			q := l.px[i]
			if q == 0 {
				continue
			}
			v := dequantize(q)
			if v >= opts.SaturationLevel {
				continue
			}
			vals = append(vals, v/float32(l.exposure)-l.bg)
			idx = append(idx, j)
		}
		if len(vals) == 0 {
			continue
		}
		var med float32
		if len(vals) >= 3 {
			sorted = append(sorted[:0], vals...)
			slices.Sort(sorted)
			med = sorted[len(sorted)/2]
		}
		for n, x := range vals {
			l := all[idx[n]]
			if len(vals) >= 3 && abs32(x-med) > k*l.noise+RelativeTolerance*abs32(med) {
				continue
			}
			wf := float32(l.weight)
			wOld := acc.Weight[i]
			wNew := wOld + wf
			delta := x - acc.Mean[i]
			r := delta * wf / wNew
			acc.Mean[i] += r
			acc.M2[i] += wOld * delta * r
			acc.Weight[i] = wNew
			acc.Count[i]++
		}
	}
	for _, l := range all {
		acc.BackgroundSum += l.weight * float64(l.bg)
		acc.WeightSum += l.weight
		acc.Subs++
	}
	return acc
}

// rebuildStreaming reads the subs once per pass, one at a time, so memory
// stays at a few frames no matter how many subs there are. The first pass
// has no rejection; each later pass rejects against the previous one.
func (p *Pipeline) rebuildStreaming(ctx context.Context, dir string, subs []storedSub, passes int) (*Accumulator, error) {
	var prev *Accumulator
	for pass := range passes {
		var acc *Accumulator
		var refMean, refStd []float32
		if prev != nil {
			refMean, refStd = prev.Stats()
		}
		for i, s := range subs {
			local := filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
			if err := p.download(ctx, p.dest, s.key, local); err != nil {
				return nil, err
			}
			sub, w, h, err := readSub(local)
			os.Remove(local)
			if err != nil {
				return nil, err
			}
			if acc == nil {
				acc = NewAccumulator(w, h)
			}
			if w != acc.W || h != acc.H {
				return nil, fmt.Errorf("sub %s is %dx%d, expected %dx%d", s.key, w, h, acc.W, acc.H)
			}
			if pass == 0 {
				opts := p.opts.Stack
				opts.MinSamples = math.MaxFloat32 // no rejection on the first pass
				if _, err := acc.Add(sub, s.exposure, s.weight, opts); err != nil {
					return nil, err
				}
				continue
			}
			noise := noiseLevel(sub, p.opts.Stack.SaturationLevel)
			if _, err := acc.AddAgainst(sub, s.exposure, s.weight, refMean, refStd, noise, p.opts.Stack); err != nil {
				return nil, err
			}
		}
		prev = acc
	}
	return prev, nil
}
