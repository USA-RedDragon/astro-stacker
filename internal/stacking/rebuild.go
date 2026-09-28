package stacking

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// RelativeTolerance widens rejection on bright pixels, where seeing changes
// between subs move a star's peak far more than the noise does.
const RelativeTolerance = 0.15

// AddAgainst folds a sub in like Add, but rejects pixels against a finished
// earlier pass instead of the running statistics. A pixel with too few other
// values to judge it is kept.
//
// With leaveOut, ref already holds this sub (with the same weight), and each
// pixel is judged against the other subs only: statistics that include the
// outlier can never reject it with few subs, since one value out of n is at
// most (n-1)/√n σ from a mean it pulls, 3.2σ for 12 subs.
func (a *Accumulator) AddAgainst(sub []float32, exposure, weight float64, ref *Accumulator, leaveOut bool, noise float64, opts Options) (AddResult, error) {
	if len(sub) != a.W*a.H || len(ref.Mean) != len(sub) {
		return AddResult{}, fmt.Errorf("sub, reference and master sizes differ")
	}
	bg := background(sub, opts.SaturationLevel)
	inv := float32(1 / exposure)
	w := float32(weight)
	k := opts.RejectSigma
	floor := float32(noise / exposure)
	reject := make([]bool, len(sub))
	for i, v := range sub {
		if v == 0 || v >= opts.SaturationLevel {
			continue
		}
		x := (v - float32(bg)) * inv
		if mean, std, ok := refStats(ref, i, x, w, leaveOut); ok {
			reject[i] = abs32(x-mean) > k*max(std, floor)+RelativeTolerance*abs32(mean)
		}
	}
	return a.fold(sub, exposure, weight, bg, reject, opts), nil
}

// refStats returns pixel i's mean and standard deviation in ref, without the
// value x of weight w when leaveOut (undoing West's update). ok is false with
// fewer than three values to go on.
func refStats(ref *Accumulator, i int, x, w float32, leaveOut bool) (mean, std float32, ok bool) {
	W, mu, m2, n := ref.Weight[i], ref.Mean[i], ref.M2[i], ref.Count[i]
	if leaveOut {
		W -= w
		n--
		if n < 3 || W <= 0 {
			return 0, 0, false
		}
		mean = (ref.Weight[i]*mu - w*x) / W
		m2 -= w * (x - mean) * (x - mu)
	} else {
		if n < 3 || W <= 0 {
			return 0, 0, false
		}
		mean = mu
	}
	return mean, float32(math.Sqrt(float64(max(m2, 0) / W))), true
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
		return p.rebuildMedian(ctx, dir, stack, subs)
	}
	passes := 2
	if len(subs) < 40 {
		// With few subs a trail still pulls the first-pass mean and σ, so
		// clip once more against cleaner statistics.
		passes = 3
	}
	return p.rebuildStreaming(ctx, dir, stack, subs, passes)
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
func (p *Pipeline) rebuildMedian(ctx context.Context, dir string, stack *app.Stack, subs []storedSub) (*Accumulator, error) {
	var all []memSub
	var w, h int
	sat := p.opts.Stack.SaturationLevel
	for i, s := range subs {
		p.progress(stack.Object, stack.Filter, StageRebuilding, i, len(subs))
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
	k := opts.RejectSigma
	// First mark each sub's outliers, so the rejection can be grown.
	reject := make([][]bool, len(all))
	for j := range reject {
		reject[j] = make([]bool, w*h)
	}
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
		if len(vals) < 3 {
			continue
		}
		sorted = append(sorted[:0], vals...)
		slices.Sort(sorted)
		med := sorted[len(sorted)/2]
		for n, x := range vals {
			if abs32(x-med) > k*all[idx[n]].noise+RelativeTolerance*abs32(med) {
				reject[idx[n]][i] = true
			}
		}
	}
	acc := NewAccumulator(w, h)
	for j, l := range all {
		reject[j] = grow(reject[j], w, h, opts.RejectGrow)
		for i, q := range l.px {
			if q == 0 || reject[j][i] {
				continue
			}
			v := dequantize(q)
			if v >= opts.SaturationLevel {
				continue
			}
			x := v/float32(l.exposure) - l.bg
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
		reject[j] = nil
		acc.BackgroundSum += l.weight * float64(l.bg)
		acc.WeightSum += l.weight
		acc.Subs++
	}
	return acc
}

// rebuildStreaming reads the subs once per pass, one at a time, so memory
// stays at a few frames no matter how many subs there are.
func (p *Pipeline) rebuildStreaming(ctx context.Context, dir string, stack *app.Stack, subs []storedSub, passes int) (*Accumulator, error) {
	loaded := 0
	return streamStack(subs, passes, p.opts.Stack, func(i int, s storedSub) ([]float32, int, int, error) {
		p.progress(stack.Object, stack.Filter, StageRebuilding, loaded, passes*len(subs))
		loaded++
		local := filepath.Join(dir, fmt.Sprintf("s%04d.fit", i))
		if err := p.download(ctx, p.dest, s.key, local); err != nil {
			return nil, 0, 0, err
		}
		defer os.Remove(local)
		return readSub(local)
	})
}

// streamStack stacks subs from load in passes. The first pass has no
// rejection; each later pass rejects against the previous one.
func streamStack(subs []storedSub, passes int, stackOpts Options, load func(int, storedSub) ([]float32, int, int, error)) (*Accumulator, error) {
	var prev *Accumulator
	for pass := range passes {
		var acc *Accumulator
		for i, s := range subs {
			sub, w, h, err := load(i, s)
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
				opts := stackOpts
				opts.MinSamples = math.MaxFloat32 // no rejection on the first pass
				if _, err := acc.Add(sub, s.exposure, s.weight, opts); err != nil {
					return nil, err
				}
				continue
			}
			// The second pass judges each sub against the others in the
			// unrejected first pass; later ones against the cleaner pass
			// before, which no longer holds the outliers.
			noise := noiseLevel(sub, stackOpts.SaturationLevel)
			if _, err := acc.AddAgainst(sub, s.exposure, s.weight, prev, pass == 1, noise, stackOpts); err != nil {
				return nil, err
			}
		}
		prev = acc
	}
	return prev, nil
}
