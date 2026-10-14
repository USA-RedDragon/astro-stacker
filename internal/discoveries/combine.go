package discoveries

import (
	"context"
	"math"
	"sync"
)

const (
	clipSigma   = 3.0
	clipMinSubs = 3
)

// Loader returns sub i ready to stack (background subtracted, scaled to
// the master's flux, NaN where it has no data) and its weight.
type Loader func(ctx context.Context, i int) (*Plane, float64, error)

// ClippedMean stacks n subs into a weighted mean, leaving out each pixel's
// values more than 3 sigma from the first pass's weighted mean and
// standard deviation. It reads every sub twice and holds only a few planes,
// so it works on any number of subs. Pixels covered by fewer than half the
// subs (and at least 2) are NaN.
func ClippedMean(ctx context.Context, w, h, n int, load Loader, threads int) (*Plane, error) {
	size := w * h
	norm := make([]float32, n)
	var first float64
	mean := make([]float32, size)
	m2 := make([]float32, size)
	wsum := make([]float32, size)
	cnt := make([]uint16, size)
	err := stream(ctx, n, load, func(i int, p *Plane, weight float64) {
		if first == 0 {
			first = weight
		}
		norm[i] = float32(weight / first)
		wi := norm[i]
		parallelRows(h, threads, func(y0, y1 int) {
			for j := y0 * w; j < y1*w; j++ {
				v := p.Data[j]
				if isNaN32(v) {
					continue
				}
				ws := wsum[j] + wi
				d := v - mean[j]
				mean[j] += wi / ws * d
				m2[j] += wi * d * (v - mean[j])
				wsum[j] = ws
				cnt[j]++
			}
		})
	})
	if err != nil {
		return nil, err
	}
	parallelRows(h, threads, func(y0, y1 int) {
		for j := y0 * w; j < y1*w; j++ {
			if wsum[j] > 0 {
				m2[j] = float32(math.Sqrt(math.Max(float64(m2[j]/wsum[j]), 0)))
			}
			wsum[j] = 0
		}
	})
	sd := m2
	sum := make([]float32, size)
	cov := cnt
	full := make([]uint16, size)
	copy(full, cnt)
	for j := range cov {
		cov[j] = 0
	}
	err = stream(ctx, n, load, func(i int, p *Plane, _ float64) {
		wi := norm[i]
		parallelRows(h, threads, func(y0, y1 int) {
			for j := y0 * w; j < y1*w; j++ {
				v := p.Data[j]
				if isNaN32(v) {
					continue
				}
				if full[j] >= clipMinSubs && float64(abs32(v-mean[j])) > clipSigma*float64(sd[j]) && sd[j] > 0 {
					continue
				}
				sum[j] += wi * v
				wsum[j] += wi
				cov[j]++
			}
		})
	})
	if err != nil {
		return nil, err
	}
	minCov := uint16(min(max(2, int(math.Ceil(0.5*float64(n)))), math.MaxUint16))
	out := &Plane{W: w, H: h, Data: sum}
	parallelRows(h, threads, func(y0, y1 int) {
		for j := y0 * w; j < y1*w; j++ {
			if cov[j] < minCov || wsum[j] <= 0 {
				sum[j] = float32(math.NaN())
			} else {
				sum[j] /= wsum[j]
			}
		}
	})
	return out, nil
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
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
