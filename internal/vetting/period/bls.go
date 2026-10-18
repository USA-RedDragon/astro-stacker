package period

import (
	"math"
)

// blsDurations are the box lengths in days the box search tries, those
// under a quarter of the period.
func blsDurations() []float64 { return []float64{0.01, 0.02, 0.04, 0.08, 0.15, 0.3, 0.6, 1.0, 1.6} }

const (
	blsOversample = 20
	blsMaxBins    = 1 << 18
	nightCap      = 3.0
)

// box is the best box shaped dip at a fixed period: its signal to noise
// ratio (depth over its error, in flux), depth in magnitudes, mid time and
// length.
type box struct {
	snr      float64
	depthMag float64
	t0, dur  float64
}

func (b box) inside(t, period float64) bool {
	x := (t - b.t0) / period
	return math.Abs(x-math.Round(x))*period < b.dur/2
}

// boxAt limits a box search to boxes whose middle falls within tol of one
// of the phases at, in cycles of the folded time t/period.
type boxAt struct {
	at  []float64
	tol float64
}

func (w *boxAt) allows(ph float64) bool {
	if w == nil {
		return true
	}
	for _, c := range w.at {
		if math.Abs(wrapPhase(ph-c)) <= w.tol {
			return true
		}
	}
	return false
}

// boxSearch is the box least squares search at one period, over the box
// mid time and length, best by signal to noise. weight, when set, scales
// each point's inverse variance; where, when set, limits the mid times.
func boxSearch(p []pt, period float64, weight []float64, where *boxAt) box {
	var durs []float64
	for _, d := range blsDurations() {
		if d < 0.25*period {
			durs = append(durs, d)
		}
	}
	if len(durs) == 0 || len(p) < 10 {
		return box{}
	}
	bw := durs[0] / blsOversample
	nb := int(math.Ceil(period / bw))
	if nb > blsMaxBins {
		nb = blsMaxBins
		bw = period / float64(nb)
	}
	sw := make([]float64, nb)
	sy := make([]float64, nb)
	var tw, ty float64
	for i, x := range p {
		f := math.Pow(10, -0.4*x.y)
		e := x.e * f / 1.0857
		w := 1 / (e * e)
		if weight != nil {
			w *= weight[i]
		}
		b := int(frac(x.t/period) * float64(nb))
		if b >= nb {
			b = nb - 1
		}
		sw[b] += w
		sy[b] += w * f
		tw += w
		ty += w * f
	}
	cw := make([]float64, 2*nb+1)
	cy := make([]float64, 2*nb+1)
	for i := range 2 * nb {
		cw[i+1] = cw[i] + sw[i%nb]
		cy[i+1] = cy[i] + sy[i%nb]
	}
	best := box{}
	bestSNR := math.Inf(-1)
	for _, d := range durs {
		k := max(1, int(math.Round(d/bw)))
		if k >= nb {
			continue
		}
		for s := range nb {
			if !where.allows((float64(s) + float64(k)/2) * bw / period) {
				continue
			}
			win := cw[s+k] - cw[s]
			out := tw - win
			if win <= 0 || out <= 0 {
				continue
			}
			yin := (cy[s+k] - cy[s]) / win
			yout := (ty - (cy[s+k] - cy[s])) / out
			dep := yout - yin
			snr := dep / math.Sqrt(1/win+1/out)
			if snr > bestSNR {
				bestSNR = snr
				best = box{snr: snr, depthMag: -2.5 * math.Log10(math.Max(1-dep/yout, 1e-3)), t0: (float64(s) + float64(k)/2) * bw, dur: float64(k) * bw}
			}
		}
	}
	return best
}

// nightWeights caps each night's total weight in a box search at nightCap
// points' worth, so one densely sampled night counts as no more than a
// few ordinary ones.
func nightWeights(p []pt) []float64 {
	n := map[int64]int{}
	for _, x := range p {
		n[nightOf(x.t)]++
	}
	out := make([]float64, len(p))
	for i, x := range p {
		out[i] = math.Min(1, nightCap/float64(n[nightOf(x.t)]))
	}
	return out
}

// nightOf numbers the local night of a time in BJD, for observatories in
// the Americas: the day turns at about 16:48 UTC.
func nightOf(bjd float64) int64 { return int64(math.Floor(bjd - 0.2)) }

// boxNights counts the distinct nights with points inside the box.
func boxNights(p []pt, period float64, b box) int {
	n := map[int64]bool{}
	for _, x := range p {
		if b.dur > 0 && b.inside(x.t, period) {
			n[nightOf(x.t)] = true
		}
	}
	return len(n)
}

// seasonDepths counts the seasons with at least 3 points in the box and
// those among them where the box is fainter than the rest.
func seasonDepths(p []pt, period float64, b box) (int, int) {
	n, pos := 0, 0
	for _, s := range seasonsOf(p) {
		if len(s) < 30 {
			continue
		}
		var in, out []float64
		for _, x := range s {
			if b.inside(x.t, period) {
				in = append(in, x.y)
			} else {
				out = append(out, x.y)
			}
		}
		if len(in) < 3 || len(out) == 0 {
			continue
		}
		n++
		if median(in)-median(out) > 0 {
			pos++
		}
	}
	return n, pos
}
