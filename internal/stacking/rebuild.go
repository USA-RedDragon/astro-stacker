package stacking

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
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
	return a.addAgainst(sub, exposure, weight, ref, leaveOut, noise, opts, nil)
}

// addAgainst is AddAgainst that also folds the sub, unrejected and with the
// same sky, into plain when it isn't nil (see keepMajority).
func (a *Accumulator) addAgainst(sub []float32, exposure, weight float64, ref *Accumulator, leaveOut bool, noise float64, opts Options, plain *Accumulator) (AddResult, error) {
	if len(sub) != a.W*a.H || len(ref.Mean) != len(sub) {
		return AddResult{}, fmt.Errorf("sub, reference and master sizes differ")
	}
	sky := a.skyFor(sub, exposure, ref, opts)
	norm := sky.normalizer(exposure)
	// ref holds this sub at its scaled weight (see fold).
	w := float32(weight * sky.scale * sky.scale)
	k := opts.RejectSigma
	floor := float32(noise / exposure / sky.scale)
	reject := make([]bool, len(sub))
	for i, v := range sub {
		if v == 0 || v >= opts.SaturationLevel {
			continue
		}
		x := norm.at(i, v)
		if mean, std, ok := refStats(ref, i, x, w, leaveOut); ok {
			reject[i] = abs32(x-mean) > k*max(std, floor)+RelativeTolerance*abs32(mean)
		}
	}
	if plain != nil {
		plain.fold(sub, exposure, weight, sky, make([]bool, len(sub)), opts)
	}
	return a.fold(sub, exposure, weight, sky, reject, opts), nil
}

// keepMajority puts back, from plain (the same subs folded without
// rejection), every pixel where rejection left fewer than half of the
// samples: see keepMajorityMem.
func keepMajority(acc, plain *Accumulator) {
	for i, n := range plain.Count {
		if acc.Count[i]*2 < n {
			acc.Weight[i], acc.Mean[i], acc.M2[i], acc.Count[i] = plain.Weight[i], plain.Mean[i], plain.M2[i], n
		}
	}
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
	return float64(nth32(s, len(s)/2)) * madToSigma
}

const madToSigma = 1.4826

type storedSub struct {
	key      string
	exposure float64
	weight   float64
}

// rejectMethod is how rebuilds reject pixels. 2: a pixel keeps all its
// samples when rejection would drop most of them (keepMajority). 4: a
// sub's shadows are filled in (shadowFill).
const rejectMethod = 4

// rejectMethodRestack is the newest rejectMethod whose older masters
// markOldRejection restacks on its own. Shadows are rare, so masters built
// before rejectMethod 4 are restacked only when asked.
const rejectMethodRestack = 3

const rejectMethodKeepMajority = 2

// rejectMethodMaxSubs bounds the masters restacked for an older rejectMethod:
// the rule only bites with few subs, and bigger masters pick it up at their
// next rebuild.
const rejectMethodMaxSubs = 40

// markMiscounted marks for the moon sweep to restack the masters whose
// stacked sub count differs from their added subs: a batch that published
// a master loaded before a sweep took subs out of it cleared the sweep's
// mark, and left those subs in it.
func (p *Pipeline) markMiscounted(ctx context.Context) {
	res := p.db.WithContext(ctx).Model(&app.Stack{}).
		Where("state_key IS NOT NULL AND needs_rebuild IS NOT TRUE AND subs <> (SELECT COUNT(*) FROM stack_frames sf "+
			"WHERE sf.stack_id = stacks.id AND sf.status = ? AND sf.registered_key IS NOT NULL)", app.StackStatusAdded).
		UpdateColumn("needs_rebuild", true)
	if res.Error != nil {
		slog.Warn("Could not mark miscounted masters for restacking", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Info("Marked masters holding subs taken out of them for restacking", "masters", res.RowsAffected)
	}
}

// markOldRejection marks masters rebuilt with an older rejectMethod for the
// moon sweep to restack.
func (p *Pipeline) markOldRejection(ctx context.Context) {
	res := p.db.WithContext(ctx).Model(&app.Stack{}).
		Where("state_key IS NOT NULL AND ((subs < ? AND (reject_method IS NULL OR reject_method < ?)) OR (subs < ? AND (reject_method IS NULL OR reject_method < ?)))",
			rejectMethodMaxSubs, rejectMethodKeepMajority, WarmUpSubs, rejectMethodRestack).
		UpdateColumn("needs_rebuild", true)
	if res.Error != nil {
		slog.Warn("Could not mark masters for restacking", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Info("Marked masters for restacking with the new rejection", "masters", res.RowsAffected)
	}
}

// rebuild recomputes a master from every stored registered sub with proper
// rejection, replacing whatever the incremental updates accumulated.
func (p *Pipeline) rebuild(ctx context.Context, stack *app.Stack) (acc *Accumulator, err error) {
	// The mark is taken before the subs are read, so a sweep that takes a
	// sub out while this runs leaves it set for the next rebuild.
	if err := p.markRebuild(ctx, stack.ID, false); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if e := p.markRebuild(context.WithoutCancel(ctx), stack.ID, true); e != nil {
				slog.Warn("Could not mark the master to be rebuilt again", "object", stack.Object, "filter", stack.Filter, "error", e)
			}
		}
	}()
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
	gains, err := p.frameGains(ctx, rows)
	if err != nil {
		return nil, err
	}
	stack.RejectMethod = rejectMethod
	dir, err := os.MkdirTemp(p.workDir, "rebuild-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cache := p.newSubCache(dir)
	defer cache.close()
	// Subs of several gains go on the one with most weight: a sub counts as
	// exposed as long as the ADU it records there (see gain.go).
	scales, err := p.gainScales(ctx, cache, stack, subs, gains)
	if err != nil {
		return nil, err
	}
	for i := range subs {
		subs[i].exposure *= scales.scale(gains[i])
	}
	stack.GainMethod, stack.GainScales = gainMethod, scales.encode()

	if len(subs) < WarmUpSubs {
		return p.rebuildMedian(ctx, cache, stack, subs)
	}
	passes := 2
	if len(subs) < 40 {
		// With few subs a trail still pulls the first-pass mean and σ, so
		// clip once more against cleaner statistics.
		passes = 3
	}
	return p.rebuildStreaming(ctx, cache, stack, subs, passes)
}

func (p *Pipeline) markRebuild(ctx context.Context, id int, on bool) error {
	return p.db.WithContext(ctx).Model(&app.Stack{}).Where("id = ?", id).UpdateColumn("needs_rebuild", on).Error
}

// frameGains is the camera gain of each row's light, nil when unknown.
func (p *Pipeline) frameGains(ctx context.Context, rows []app.StackFrame) ([]*float64, error) {
	ids := make([]int, len(rows))
	for i, r := range rows {
		ids[i] = r.FrameID
	}
	var frames []app.Frame
	if err := p.db.WithContext(ctx).Select("id", "gain").Where("id IN ?", ids).Find(&frames).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]*float64, len(frames))
	for _, f := range frames {
		byID[f.ID] = f.Gain
	}
	gains := make([]*float64, len(rows))
	for i, r := range rows {
		gains[i] = byID[r.FrameID]
	}
	return gains, nil
}

func quantize(v float32) uint16 { return imagedata.Quantize16(v) }

func dequantize(q uint16) float32 { return imagedata.Dequantize16(q) }

// memSub is one sub held in memory for a median-anchored rebuild.
type memSub struct {
	key      string
	px       []uint16 // quantize()d samples, 0 = empty
	bg       float32  // background per second
	noise    float32  // background noise per second
	sky      skyModel
	fill     *shadowFill
	exposure float64
	weight   float64
}

// rebuildMedian holds every sub in memory as 16-bit samples and anchors
// rejection on the per-pixel median, which a single outlier can't move.
func (p *Pipeline) rebuildMedian(ctx context.Context, cache *subCache, stack *app.Stack, subs []storedSub) (*Accumulator, error) {
	var all []memSub
	var w, h int
	sat := p.opts.Stack.SaturationLevel
	for i, s := range subs {
		p.progress(stack.Object, stack.Filter, StageRebuilding, i, len(subs))
		sub, sw, sh, err := cache.load(ctx, s.key)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			w, h = sw, sh
		} else if sw != w || sh != h {
			return nil, fmt.Errorf("sub %s is %dx%d, expected %dx%d", s.key, sw, sh, w, h)
		}
		l := toMemSub(sub, s.exposure, s.weight, sat)
		l.key = s.key
		all = append(all, l)
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
	for j := range all {
		all[j].sky = flatSky(w, h, float64(all[j].bg))
	}
	var ln LocalNormRecord
	if opts.LocalNorm && len(all) >= 3 {
		// Each sub's sky is fitted against all of them stacked with their
		// background levels, which averages their gradients.
		ref := NewAccumulator(w, h)
		for _, l := range all {
			foldMem(ref, l, nil, opts.SaturationLevel)
		}
		ln.Uneven, ln.Measured = unevenShare(ref)
		for j := range all {
			if !needsLocalNorm(ref) {
				break
			}
			l := &all[j]
			l.sky = fitSky(func(i int) float32 { return dequantize(l.px[i]) }, w, h, l.exposure, ref, opts.SaturationLevel, float64(l.bg))
		}
	}
	if opts.FillShadows {
		fillShadowsMem(all, w, h, opts.SaturationLevel)
	}
	norms := make([]*normalizer, len(all))
	// First mark each sub's outliers, so the rejection can be grown.
	reject := make([][]bool, len(all))
	for j := range reject {
		reject[j] = make([]bool, w*h)
		norms[j] = all[j].sky.normalizer(all[j].exposure)
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
			vals = append(vals, norms[j].at(i, v))
			idx = append(idx, j)
		}
		if len(vals) < 3 {
			continue
		}
		sorted = append(sorted[:0], vals...)
		slices.Sort(sorted)
		med := sorted[len(sorted)/2]
		for n, x := range vals {
			l := all[idx[n]]
			if abs32(x-med) > k*l.noise/float32(l.sky.scale)+RelativeTolerance*abs32(med) {
				reject[idx[n]][i] = true
			}
		}
	}
	for j := range reject {
		reject[j] = grow(reject[j], w, h, opts.RejectGrow)
	}
	if opts.KeepMajority {
		keepMajorityMem(all, reject, opts.SaturationLevel)
	}
	acc := NewAccumulator(w, h)
	acc.LocalNorm.Uneven, acc.LocalNorm.Measured = ln.Uneven, ln.Measured
	for j, l := range all {
		foldMem(acc, l, reject[j], opts.SaturationLevel)
		reject[j] = nil
	}
	return acc
}

// fillShadowsMem finds each in-memory sub's shadows against the others,
// normalized by their skies, for foldMem to fill in.
func fillShadowsMem(all []memSub, w, h int, sat float32) {
	cells := make([][]float32, len(all))
	skies := make([]float64, len(all))
	for j := range all {
		l := &all[j]
		norm := l.sky.normalizer(l.exposure)
		cells[j] = cellMedians(w, h, func(i int) (float32, bool) {
			q := l.px[i]
			if q == 0 {
				return 0, false
			}
			v := dequantize(q)
			return norm.at(i, v), v < sat
		})
		skies[j] = l.sky.mean / l.sky.scale
	}
	for j, f := range findShadows(cells, skies, w, h) {
		all[j].fill = f
		logShadows(all[j].key, f)
	}
}

func logShadows(key string, f *shadowFill) {
	if f == nil {
		return
	}
	for _, s := range f.shadows {
		slog.Info("Filled in a shadow in a sub", "sub", key, "x", s.x, "y", s.y, "cells", s.cells, "depth", s.depth)
	}
}

// keepMajorityMem clears a pixel's rejections in every sub when they would
// leave fewer than half of its usable samples. Rejection is for the odd
// outlier; on a star core, subs of different seeing all differ from the
// median by more than RelativeTolerance, and the grown rejections of the
// neighbours then take out the median sub too. With every sample gone the
// pixel was written as saturated (fillSaturated), a coloured dot per star.
func keepMajorityMem(all []memSub, reject [][]bool, sat float32) {
	if len(all) == 0 {
		return
	}
	for i := range all[0].px {
		usable, kept := 0, 0
		for j, l := range all {
			q := l.px[i]
			if q == 0 || dequantize(q) >= sat {
				continue
			}
			usable++
			if !reject[j][i] {
				kept++
			}
		}
		if kept*2 < usable {
			for j := range reject {
				reject[j][i] = false
			}
		}
	}
}

// foldMem adds an in-memory sub's pixels that aren't empty, saturated or
// rejected, normalized by its sky, as fold does.
func foldMem(acc *Accumulator, l memSub, reject []bool, sat float32) {
	norm := l.sky.normalizer(l.exposure)
	sky := l.sky.mean / l.sky.scale
	weight := l.weight * l.sky.scale * l.sky.scale
	wf := float32(weight)
	for i, q := range l.px {
		if q == 0 || (reject != nil && reject[i]) {
			continue
		}
		v := dequantize(q)
		if v >= sat {
			continue
		}
		x := norm.at(i, v)
		if l.fill != nil {
			x += float32(l.fill.at(i) * sky)
		}
		wOld := acc.Weight[i]
		wNew := wOld + wf
		delta := x - acc.Mean[i]
		r := delta * wf / wNew
		acc.Mean[i] += r
		acc.M2[i] += wOld * delta * r
		acc.Weight[i] = wNew
		acc.Count[i]++
	}
	acc.BackgroundSum += weight * l.sky.mean / l.sky.scale
	acc.WeightSum += weight
	acc.Subs++
	if l.sky.fitted {
		acc.LocalNorm.Subs++
	}
}

// rebuildStreaming reads the subs once per pass, one at a time, so memory
// stays at a few frames no matter how many subs there are. The cache keeps
// later passes from downloading them again.
func (p *Pipeline) rebuildStreaming(ctx context.Context, cache *subCache, stack *app.Stack, subs []storedSub, passes int) (*Accumulator, error) {
	loaded := 0
	return streamStack(subs, passes, p.opts.Stack, func(_ int, s storedSub) ([]float32, int, int, error) {
		p.progress(stack.Object, stack.Filter, StageRebuilding, loaded, passes*len(subs))
		loaded++
		return cache.load(ctx, s.key)
	})
}

// subCache keeps a rebuild's subs on the scratch volume after their first
// download, so its later passes read them from disk. Every rebuild's cache
// shares the pipeline's RebuildCache bytes, and a sub is only kept while
// the volume has RebuildCacheFree bytes left; the rest are downloaded on
// every pass. The first subs to arrive are the ones kept: the passes read
// in the same order, so evicting for later ones would save nothing.
type subCache struct {
	p    *Pipeline
	dir  string
	kept map[string]string
	n    int
	held int64
}

func (p *Pipeline) newSubCache(dir string) *subCache {
	return &subCache{p: p, dir: dir, kept: map[string]string{}}
}

func (c *subCache) load(ctx context.Context, key string) ([]float32, int, int, error) {
	if file, ok := c.kept[key]; ok {
		return readSub(file)
	}
	c.n++
	file := filepath.Join(c.dir, fmt.Sprintf("c%05d.fit", c.n))
	if err := c.p.download(ctx, c.p.dest, key, file); err != nil {
		return nil, 0, 0, err
	}
	sub, w, h, err := readSub(file)
	if err == nil && c.keep(file) {
		c.kept[key] = file
	} else {
		os.Remove(file)
	}
	return sub, w, h, err
}

func (c *subCache) keep(file string) bool {
	st, err := os.Stat(file)
	if err != nil {
		return false
	}
	free, err := freeSpace(c.dir)
	if err != nil || free < c.p.opts.RebuildCacheFree {
		return false
	}
	if !c.p.reserveCache(st.Size()) {
		return false
	}
	c.held += st.Size()
	return true
}

func (c *subCache) close() {
	for _, file := range c.kept {
		os.Remove(file)
	}
	c.kept = map[string]string{}
	c.p.reserveCache(-c.held)
	c.held = 0
}

// reserveCache takes n bytes of the rebuild cache budget, or gives them
// back when n is negative.
func (p *Pipeline) reserveCache(n int64) bool {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if n > 0 && p.cacheHeld+n > p.opts.RebuildCache {
		return false
	}
	p.cacheHeld += n
	return true
}

// streamStack stacks subs from load in passes. The first pass has no
// rejection; each later pass rejects against the previous one, with the
// shadows the first pass found filled in.
func streamStack(subs []storedSub, passes int, stackOpts Options, load func(int, storedSub) ([]float32, int, int, error)) (*Accumulator, error) {
	var prev *Accumulator
	var cells [][]float32
	var skies []float64
	var fills []*shadowFill
	if stackOpts.FillShadows {
		cells, skies = make([][]float32, len(subs)), make([]float64, len(subs))
	}
	for pass := range passes {
		var acc, plain *Accumulator
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
				// Nor local normalization: this pass, which averages the
				// subs' gradients, is what later passes normalize to.
				opts.LocalNorm = false
				if _, err := acc.Add(sub, s.exposure, s.weight, opts); err != nil {
					return nil, err
				}
				if cells != nil {
					bg := background(sub, stackOpts.SaturationLevel)
					skies[i] = bg / s.exposure
					inv := float32(1 / s.exposure)
					cells[i] = cellMedians(w, h, func(j int) (float32, bool) {
						v := sub[j]
						return (v - float32(bg)) * inv, v != 0 && v < stackOpts.SaturationLevel
					})
				}
				continue
			}
			if f := fills[i]; f != nil {
				sub = slices.Clone(sub)
				for j, v := range sub {
					if v != 0 && v < stackOpts.SaturationLevel {
						sub[j] = v + float32(f.at(j)*skies[i]*s.exposure)
					}
				}
			}
			// The second pass judges each sub against the others in the
			// unrejected first pass; later ones against the cleaner pass
			// before, which no longer holds the outliers.
			if pass == passes-1 && stackOpts.KeepMajority && plain == nil {
				plain = NewAccumulator(w, h)
			}
			noise := noiseLevel(sub, stackOpts.SaturationLevel)
			if _, err := acc.addAgainst(sub, s.exposure, s.weight, prev, pass == 1, noise, stackOpts, plain); err != nil {
				return nil, err
			}
		}
		if plain != nil {
			keepMajority(acc, plain)
		}
		if pass == 0 {
			fills = make([]*shadowFill, len(subs))
			if cells != nil {
				fills = findShadows(cells, skies, acc.W, acc.H)
				for i, f := range fills {
					logShadows(subs[i].key, f)
				}
			}
		}
		prev = acc
	}
	return prev, nil
}
