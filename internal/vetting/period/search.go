package period

import (
	"math"
	"runtime"
	"slices"
	"sync"
)

type candidate struct {
	period, phase, width, score float64
}

type dipSet struct {
	dipT, dipW   []float64
	normT, normW []float64
	total        float64
}

func collectDips(d *data) dipSet {
	var s dipSet
	for _, se := range d.series {
		thr := 5 * se.sigma
		norm := 2 * se.sigma
		for _, p := range se.srch {
			switch {
			case p.y > thr && p.y > 3*p.e:
				s.dipT = append(s.dipT, p.t)
				s.dipW = append(s.dipW, p.w)
				s.total += p.w
			case p.y < norm:
				s.normT = append(s.normT, p.t)
				s.normW = append(s.normW, p.w)
			}
		}
	}
	return s
}

func parallel(n int, fn func(lo, hi int)) {
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if n < 1024 || workers < 2 {
		fn(0, n)
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Go(func() { fn(lo, hi) })
	}
	wg.Wait()
}

func parallelChunks(n int, fn func(c int)) {
	workers := min(runtime.GOMAXPROCS(0), 16, n)
	if workers < 2 {
		for c := range n {
			fn(c)
		}
		return
	}
	var wg sync.WaitGroup
	next := make(chan int, n)
	for c := range n {
		next <- c
	}
	close(next)
	for range workers {
		wg.Go(func() {
			for c := range next {
				fn(c)
			}
		})
	}
	wg.Wait()
}

func frac(x float64) float64 { return x - math.Floor(x) }

func wrapPhase(x float64) float64 { return x - math.Floor(x+0.5) }

func histScan(ds dipSet, freqs []float64, width float64) ([]float64, []float64) {
	score := make([]float64, len(freqs))
	phase := make([]float64, len(freqs))
	parallel(len(freqs), func(lo, hi int) {
		h := make([]float64, 4000)
		for i := lo; i < hi; i++ {
			p := 1 / freqs[i]
			nb := int(math.Max(8, math.Floor(p/width)))
			if nb > 4000 {
				nb = 4000
			}
			hh := h[:nb]
			clear(hh)
			f := freqs[i]
			for k, t := range ds.dipT {
				b := int(frac(t*f) * float64(nb))
				if b >= nb {
					b = nb - 1
				}
				hh[b] += ds.dipW[k]
			}
			best, bj := hh[nb-1]+hh[0], nb-1
			for j := range nb - 1 {
				if v := hh[j] + hh[j+1]; v > best {
					best, bj = v, j
				}
			}
			mu := ds.total * 2 / float64(nb)
			score[i] = (best - mu) / math.Sqrt(mu+1)
			phase[i] = float64(bj+1) / float64(nb)
		}
	})
	return score, phase
}

func refineScore(ds dipSet, p, center, width float64) (float64, float64) {
	half := 0.5 * width / p
	var sx, sy, sw float64
	for k, t := range ds.dipT {
		u := wrapPhase(t/p - center)
		if math.Abs(u) < half {
			a := 2 * math.Pi * (center + u)
			sx += ds.dipW[k] * math.Cos(a)
			sy += ds.dipW[k] * math.Sin(a)
			sw += ds.dipW[k]
		}
	}
	if sw > 0 {
		center = frac(math.Atan2(sy, sx) / (2 * math.Pi))
	}
	var in, bad float64
	for k, t := range ds.dipT {
		if math.Abs(wrapPhase(t/p-center)) < half {
			in += ds.dipW[k]
		}
	}
	core := 0.3 * width / p
	for k, t := range ds.normT {
		if math.Abs(wrapPhase(t/p-center)) < core {
			bad += ds.normW[k]
		}
	}
	return in - bad, center
}

func freqGrid(fmin, fmax, step float64) []float64 {
	var f []float64
	for x := fmin; x <= fmax; x *= 1 + step {
		f = append(f, x)
	}
	return f
}

func localMaxima(score []float64, top int) []int {
	var idx []int
	for i := range score {
		l := i == 0 || score[i] >= score[i-1]
		r := i == len(score)-1 || score[i] > score[i+1]
		if l && r && score[i] > 0 {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch {
		case score[a] > score[b]:
			return -1
		case score[a] < score[b]:
			return 1
		}
		return a - b
	})
	if len(idx) > top {
		idx = idx[:top]
	}
	return idx
}

func dipSearch(d *data, minP, maxP float64) []candidate {
	ds := collectDips(d)
	if ds.total < 3 {
		return nil
	}
	span := d.t1 - d.t0
	var cands []candidate
	for _, sc := range []struct{ width, longest float64 }{{0.05, 5}, {0.15, 30}, {0.4, math.Inf(1)}} {
		width := sc.width
		top := math.Min(maxP, sc.longest)
		if width > 0.25*top || top <= minP {
			continue
		}
		step := width / 4 / span
		freqs := freqGrid(1/top, 1/minP, step)
		score, phase := histScan(ds, freqs, width)
		for _, i := range localMaxima(score, 300) {
			p := 1 / freqs[i]
			sc, c := refineScore(ds, p, phase[i], width)
			cands = append(cands, candidate{period: p, phase: c, width: width, score: sc})
		}
	}
	slices.SortStableFunc(cands, func(a, b candidate) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	var out []candidate
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		dup := false
		for _, o := range out {
			if math.Abs(c.period-o.period)/o.period < 2e-3 {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		pc := polishCandidate(ds, c, span)
		for _, o := range out {
			if math.Abs(pc.period-o.period)/o.period < 2e-3 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, pc)
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return 0
	})
	for i, c := range out {
		out[i] = preferShorter(ds, c, span, minP)
	}
	return out
}

func polishCandidate(ds dipSet, c candidate, span float64) candidate {
	step := c.width / 4 / span * c.period
	best := c
	for k := -16; k <= 16; k++ {
		p := c.period + float64(k)*step/8
		sc, ph := refineScore(ds, p, c.phase, c.width)
		if sc > best.score {
			best = candidate{period: p, phase: ph, width: c.width, score: sc}
		}
	}
	return best
}

func preferShorter(ds dipSet, c candidate, span, minP float64) candidate {
	for _, k := range []float64{3, 2} {
		p := c.period / k
		if p < minP {
			continue
		}
		best := candidate{score: math.Inf(-1)}
		for _, w := range []float64{c.width, c.width / 2} {
			sub := polishCandidate(ds, candidate{period: p, phase: frac(c.phase * k), width: w, score: math.Inf(-1)}, span)
			if sub.score > best.score {
				best = sub
			}
		}
		if best.score >= 0.9*c.score {
			return best
		}
	}
	return c
}
