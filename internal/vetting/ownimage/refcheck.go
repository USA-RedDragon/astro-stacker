package ownimage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
)

const (
	outlierSpread = 5.0
	refShare      = 0.5
	refSignif     = 3.0
	hotPeakShare  = 0.6
	minRefSubs    = 3
	refStarNoise  = 5.0
)

// level is a sub's light in the small aperture at the spot, on the
// ensemble's footing and corrected for the share of a star's light the
// aperture holds at the sub's star width.
func level(sp *spotPlan, e *ensemble, i int) (lv, noise, peak float64, ok bool) {
	r := sp.recs[i]
	t, has := e.transp[i]
	if r == nil || !r.apOK || !has || !(t > 0) || !(r.sigma > 0) {
		return 0, 0, 0, false
	}
	t *= 1 - math.Exp(-apR*apR/(2*r.sigma*r.sigma))
	return r.ap / t, r.noise * math.Sqrt(math.Pi*apR*apR) / t, r.peak / t, true
}

// refEval is one flagged night and filter's reference, rebuilt.
type refEval struct {
	set                  refSet
	refAll               float64
	shareMean, shareClip float64
	signif               float64
	outliers             []int
	hot                  []bool
	refs                 int
	ok                   bool
}

// stackAt stacks the subs' stamps, scaled to the ensemble, as the
// discoveries runner stacks a reference (clipped) or as older runs did (a
// weighted mean), and measures the small aperture at the spot in it.
func stackAt(ctx context.Context, sp *spotPlan, e *ensemble, idx []int, clip bool) (float64, bool) {
	var planes []*discoveries.Plane
	var weights []float64
	for _, i := range idx {
		st := scaledStamp(sp, e, i)
		r := sp.recs[i]
		if st == nil || !(r.noise > 0) {
			continue
		}
		p := discoveries.NewPlane(stampSide, stampSide)
		for k, v := range st {
			p.Data[k] = float32(v)
		}
		planes = append(planes, p)
		n := r.noise / e.transp[i]
		weights = append(weights, 1/(n*n))
	}
	if len(planes) == 0 {
		return 0, false
	}
	load := func(_ context.Context, i int) (*discoveries.Plane, float64, error) { return planes[i], weights[i], nil }
	var st *discoveries.Plane
	if clip {
		var err error
		if st, err = discoveries.ClippedMean(ctx, stampSide, stampSide, len(planes), load, 1); err != nil {
			return 0, false
		}
	} else {
		st = discoveries.NewPlane(stampSide, stampSide)
		for k := range st.Data {
			var sum, w float64
			for j, p := range planes {
				if v := p.Data[k]; !isNaN32(v) {
					sum += weights[j] * float64(v)
					w += weights[j]
				}
			}
			st.Data[k] = float32(sum / w)
		}
	}
	cx, cy := stampHalf+sp.x-math.Round(sp.x), stampHalf+sp.y-math.Round(sp.y)
	return discoveries.Aperture(st, cx, cy, apR), true
}

func evalRef(ctx context.Context, sp *spotPlan, e *ensemble, set refSet) refEval {
	ev := refEval{set: set}
	var lv, noise, peaks []float64
	var idx []int
	for _, i := range set.refs {
		if v, n, pk, ok := level(sp, e, i); ok {
			lv, noise, peaks, idx = append(lv, v), append(noise, n), append(peaks, pk), append(idx, i)
		}
	}
	var nv []float64
	for _, i := range set.nights {
		if v, _, _, ok := level(sp, e, i); ok {
			nv = append(nv, v)
		}
	}
	ev.refs = len(lv)
	if len(lv) < minRefSubs || len(nv) == 0 {
		return ev
	}
	med := median(lv)
	spread := math.Max(robustStd(lv), median(noise))
	dir := -float64(sp.fadeSign)
	medPeak := median(peaks)
	var clean []int
	var cleanLv []float64
	for k, v := range lv {
		d := v - med
		out := dir*d > outlierSpread*spread
		if dir == 0 {
			out = math.Abs(d) > outlierSpread*spread
		}
		if !out {
			clean, cleanLv = append(clean, idx[k]), append(cleanLv, v)
			continue
		}
		ev.outliers = append(ev.outliers, idx[k])
		ev.hot = append(ev.hot, d != 0 && (peaks[k]-medPeak)/d >= hotPeakShare)
	}
	if len(clean) == 0 {
		return ev
	}
	ev.refAll = mean(lv)
	ev.signif = math.Abs(ev.refAll-mean(cleanLv)) / (spread / math.Sqrt(float64(len(lv))))
	ev.ok = true
	if len(ev.outliers) == 0 {
		return ev
	}
	night, okN := stackAt(ctx, sp, e, set.nights, false)
	for _, clip := range []bool{false, true} {
		with, ok1 := stackAt(ctx, sp, e, idx, clip)
		without, ok2 := stackAt(ctx, sp, e, clean, clip)
		if !ok1 || !ok2 || !okN {
			continue
		}
		gap := with - night
		if dm, ok := sp.reported(set.night, set.filter); ok && without > refStarNoise*spread*median(apFrac(sp, idx)) {
			gap = without * (1 - math.Pow(10, -0.4*dm))
		}
		share := 0.0
		if gap != 0 {
			share = (with - without) / gap
		}
		if clip {
			ev.shareClip = share
		} else {
			ev.shareMean = share
		}
	}
	return ev
}

// apFrac is the share of a star's light the small aperture holds in each
// sub at its star width.
func apFrac(sp *spotPlan, idx []int) []float64 {
	out := make([]float64, 0, len(idx))
	for _, i := range idx {
		if s := sp.recs[i].sigma; s > 0 {
			out = append(out, 1-math.Exp(-apR*apR/(2*s*s)))
		}
	}
	return out
}

// refClean says whether stray light in a few reference subs made the
// change: when it does so even through the clipping the runner stacks
// with, or through a plain mean, as older runs stacked, while the star's
// own light curve shows no change.
func refClean(ctx context.Context, subs []Sub, sp *spotPlan, e *ensemble, ownFlat bool) (check.Check, []refEval) {
	var evals []refEval
	refs := map[int]bool{}
	for _, set := range sp.sets {
		ev := evalRef(ctx, sp, e, set)
		if ev.ok {
			evals = append(evals, ev)
			for _, i := range set.refs {
				refs[i] = true
			}
		}
	}
	if len(evals) == 0 {
		return check.Skipped(check.RefClean, "Not measured yet. Too few of the reference subs could be measured at this spot."), nil
	}
	bad := map[int]bool{}
	hot := map[int]bool{}
	var shares []float64
	for _, ev := range evals {
		if len(ev.outliers) == 0 || ev.signif < refSignif {
			continue
		}
		share := ev.shareClip
		if ownFlat && ev.shareMean > share {
			share = ev.shareMean
		}
		if share < refShare {
			continue
		}
		shares = append(shares, math.Min(share, 1))
		for k, i := range ev.outliers {
			bad[i] = true
			if ev.hot[k] {
				hot[i] = true
			}
		}
	}
	if len(bad) > 0 {
		what := "Extra light"
		if 2*len(hot) > len(bad) {
			what = "Hot pixels"
		}
		return check.New(check.RefClean, check.Fail, fmt.Sprintf("%s at this spot in %s subs from %s went into the reference image. Leaving those %d subs out takes away %.0f%% of the change.",
			what, filterList(subs, bad), nightList(subs, bad), len(bad), 100*mean(shares))), evals
	}
	var stray []int
	var maxShare float64
	for _, ev := range evals {
		stray = append(stray, ev.outliers...)
		if len(ev.outliers) > 0 {
			maxShare = math.Max(maxShare, math.Max(ev.shareClip, 0))
		}
	}
	if n := len(uniq(stray)); n > 0 {
		them := "them"
		if n == 1 {
			them = "it"
		}
		return check.New(check.RefClean, check.Pass, fmt.Sprintf("%d of the %d reference subs had extra light at this spot, but leaving %s out moves the reference by only %.0f%% of the change.",
			n, len(refs), them, 100*maxShare)), evals
	}
	return check.New(check.RefClean, check.Pass, fmt.Sprintf("None of the %d reference subs stands out at this spot.", len(refs))), evals
}

func uniq(v []int) map[int]bool {
	m := map[int]bool{}
	for _, x := range v {
		m[x] = true
	}
	return m
}

func joinAnd(v []string) string {
	switch len(v) {
	case 0:
		return ""
	case 1:
		return v[0]
	case 2:
		return v[0] + " and " + v[1]
	}
	return strings.Join(v[:len(v)-1], ", ") + " and " + v[len(v)-1]
}

func filterList(subs []Sub, set map[int]bool) string {
	count := map[string]int{}
	for i := range set {
		count[subs[i].Filter]++
	}
	fs := make([]string, 0, len(count))
	for f := range count {
		fs = append(fs, f)
	}
	sort.Slice(fs, func(a, b int) bool {
		if count[fs[a]] != count[fs[b]] {
			return count[fs[a]] > count[fs[b]]
		}
		return fs[a] < fs[b]
	})
	return joinAnd(fs)
}

func nightList(subs []Sub, set map[int]bool) string {
	m := map[string]bool{}
	for i := range set {
		m[nightKey(subs[i].Night)] = true
	}
	ns := make([]string, 0, len(m))
	for n := range m {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return joinAnd(ns)
}
