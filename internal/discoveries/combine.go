package discoveries

import (
	"context"
	"math"
	"sync"
)

const (
	clipSigma   = 3.0
	clipMinSubs = 3
	clipTrim    = 4
	clipSignal  = 0.3
)

// Loader returns sub i ready to stack (background subtracted, scaled to
// the master's flux, NaN where it has no data) and its weight. A nil plane
// leaves the sub out.
type Loader func(ctx context.Context, i int) (*Plane, float64, error)

// ClippedMean stacks n subs into a weighted mean, leaving out each pixel's
// values more than 3 sigma from a robust center and spread of all the
// subs' values there: their mean and standard deviation with the 4 highest
// and 4 lowest left out (fewer when there are fewer than 9 subs), the
// spread corrected for the trimming and floored at the subs' sky noise and
// 30% of the center. A defect pixel is often high, or low, in a few subs of
// one night at the same spot, as the guiding holds still between dithers:
// four such subs in the Cygnis Loop Panel 2 reference, 0.07 to 0.55 against
// a background of 0.0006, widened a plain clip's spread enough to stay in
// (candidates 15111, 15045, 14520 and 14082, a "fading star" ZTF shows
// flat), and two in the Garlic Nebula's O-III reference (candidate 13956).
// It reads every sub twice and holds a few planes, so it works on any number
// of subs. Pixels covered by fewer than half the subs used (and at least 2)
// are NaN.
func ClippedMean(ctx context.Context, w, h, n int, load Loader, threads int) (*Plane, error) {
	return clippedMean(ctx, w, h, n, load, threads, nil)
}

// clippedMean is ClippedMean with each sub's weight in the mean, not in the
// clip, times its share, when shares is not nil.
func clippedMean(ctx context.Context, w, h, n int, load Loader, threads int, shares []float64) (*Plane, error) {
	size := w * h
	norm := make([]float32, n)
	var first float64
	var noise []float64
	sum1 := make([]float32, size)
	sumSq := make([]float64, size)
	cnt := make([]uint16, size)
	hi, lo := make([][]float32, clipTrim), make([][]float32, clipTrim)
	for k := range clipTrim {
		hi[k], lo[k] = make([]float32, size), make([]float32, size)
		for j := range size {
			hi[k][j], lo[k][j] = float32(math.Inf(-1)), float32(math.Inf(1))
		}
	}
	err := stream(ctx, n, load, func(i int, p *Plane, weight float64) {
		if p == nil || !(weight > 0) {
			return
		}
		if first == 0 {
			first = weight
		}
		norm[i] = float32(weight / first)
		if shares != nil {
			norm[i] *= float32(shares[i])
		}
		noise = append(noise, 1/math.Sqrt(weight))
		parallelRows(h, threads, func(y0, y1 int) {
			for j := y0 * w; j < y1*w; j++ {
				v := p.Data[j]
				if isNaN32(v) {
					continue
				}
				sum1[j] += v
				sumSq[j] += float64(v) * float64(v)
				cnt[j]++
				insertExtreme(hi, j, v, func(a, b float32) bool { return a > b })
				insertExtreme(lo, j, v, func(a, b float32) bool { return a < b })
			}
		})
	})
	if err != nil {
		return nil, err
	}
	used := len(noise)
	sky := median64(noise)
	sum := make([]float32, size)
	wkept := make([]float32, size)
	cov := make([]uint16, size)
	err = stream(ctx, n, load, func(i int, p *Plane, _ float64) {
		if p == nil || norm[i] == 0 {
			return
		}
		wi := norm[i]
		parallelRows(h, threads, func(y0, y1 int) {
			for j := y0 * w; j < y1*w; j++ {
				v := p.Data[j]
				if isNaN32(v) {
					continue
				}
				if c := int(cnt[j]); c >= clipMinSubs {
					m, sd := trimmed(sum1[j], sumSq[j], c, hi, lo, j)
					sd = math.Max(sd, math.Hypot(sky, clipSignal*m))
					if math.Abs(float64(v)-m) > clipSigma*sd {
						continue
					}
				}
				sum[j] += wi * v
				wkept[j] += wi
				cov[j]++
			}
		})
	})
	if err != nil {
		return nil, err
	}
	minCov := uint16(min(max(2, int(math.Ceil(0.5*float64(used)))), math.MaxUint16))
	out := &Plane{W: w, H: h, Data: sum}
	parallelRows(h, threads, func(y0, y1 int) {
		for j := y0 * w; j < y1*w; j++ {
			if cov[j] < minCov || wkept[j] <= 0 {
				sum[j] = float32(math.NaN())
			} else {
				sum[j] /= wkept[j]
			}
		}
	})
	return out, nil
}

// insertExtreme keeps ext[0..] at pixel j the most extreme values seen,
// most extreme first, by before.
func insertExtreme(ext [][]float32, j int, v float32, before func(a, b float32) bool) {
	k := len(ext) - 1
	if !before(v, ext[k][j]) {
		return
	}
	for k > 0 && before(v, ext[k-1][j]) {
		ext[k][j] = ext[k-1][j]
		k--
	}
	ext[k][j] = v
}

// trimmed is the mean and standard deviation of a pixel's c values with
// the most extreme k of each side left out, k up to clipTrim and at most
// (c-1)/2, the deviation corrected for what trimming a normal sample takes
// off it.
func trimmed(s float32, ss float64, c int, hi, lo [][]float32, j int) (mean, sd float64) {
	k := min(len(hi), (c-1)/2)
	sum, sq := float64(s), ss
	for e := range k {
		a, b := float64(hi[e][j]), float64(lo[e][j])
		sum -= a + b
		sq -= a*a + b*b
	}
	m := float64(c - 2*k)
	mean = sum / m
	v := sq/m - mean*mean
	if !(v > 0) || m < 2 {
		return mean, 0
	}
	return mean, math.Sqrt(v) * trimCorrection(float64(k)/float64(c))
}

// trimCorrection is the factor that brings the standard deviation of a
// normal sample with a share p cut from each side back to the whole
// sample's.
func trimCorrection(p float64) float64 {
	if p <= 0 {
		return 1
	}
	z := math.Sqrt2 * math.Erfinv(1-2*p)
	phi := math.Exp(-z*z/2) / math.Sqrt(2*math.Pi)
	f := 1 - 2*z*phi/(1-2*p)
	if !(f > 0) {
		return 1
	}
	return 1 / math.Sqrt(f)
}

// stream loads subs one ahead of use and hands each to use in order.
func stream(ctx context.Context, n int, load Loader, use func(i int, p *Plane, weight float64)) error {
	type item struct {
		i   int
		p   *Plane
		w   float64
		err error
	}
	ch := make(chan item, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer close(ch)
		for i := range n {
			p, wt, err := load(ctx, i)
			select {
			case ch <- item{i, p, wt, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for it := range ch {
		if it.err != nil {
			return it.err
		}
		use(it.i, it.p, it.w)
	}
	return ctx.Err()
}

// parallelRows splits rows [0, h) into threads ranges and runs fn on each.
func parallelRows(h, threads int, fn func(y0, y1 int)) {
	threads = max(1, min(threads, h))
	if threads == 1 {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	for t := range threads {
		y0, y1 := t*h/threads, (t+1)*h/threads
		wg.Go(func() { fn(y0, y1) })
	}
	wg.Wait()
}
