package period

import (
	"math"
	"slices"
)

// The robustness tests work on raw survey points per band, each series on
// its own median, with errors floored at minErr; their thresholds were
// set on ZTF data with the same floor.
const (
	minErr     = 0.005
	hpHalf     = 10.0
	seasonGap  = 60.0
	minBand    = 60
	minSeason  = 8
	cohSeason  = 60
	lsMinFreq  = 1.0 / 200
	lsMaxFreq  = 10.0
	lsGridMax  = 5.0
	lsStep     = 0.2
	lsFineStep = 0.02
	peakSep    = 2.0
)

// pt is one raw point: time in days, magnitude off its series' median,
// error.
type pt struct{ t, y, e float64 }

func sortPts(p []pt) {
	slices.SortFunc(p, func(a, b pt) int {
		switch {
		case a.t < b.t:
			return -1
		case a.t > b.t:
			return 1
		}
		return 0
	})
}

func spanOf(p []pt) float64 {
	if len(p) < 2 {
		return 0
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range p {
		lo, hi = math.Min(lo, x.t), math.Max(hi, x.t)
	}
	return hi - lo
}

// highPass takes from each point the median of the points within hpHalf
// days of it, so changes slower than about three weeks drop out.
func highPass(in []pt) []pt {
	out := make([]pt, len(in))
	lo, hi := 0, 0
	win := make([]float64, 0, 64)
	for i, p := range in {
		for in[lo].t < p.t-hpHalf {
			lo++
		}
		for hi < len(in) && in[hi].t <= p.t+hpHalf {
			hi++
		}
		win = win[:0]
		for _, q := range in[lo:hi] {
			win = append(win, q.y)
		}
		out[i] = pt{t: p.t, y: p.y - median(win), e: p.e}
	}
	return out
}

// seasonsOf splits points at gaps longer than seasonGap days.
func seasonsOf(in []pt) [][]pt {
	var out [][]pt
	start := 0
	for i := 1; i <= len(in); i++ {
		if i == len(in) || in[i].t-in[i-1].t > seasonGap {
			out = append(out, in[start:i])
			start = i
		}
	}
	return out
}

// detrendSeasons takes each season's median from its points and drops
// seasons with fewer than minSeason points.
func detrendSeasons(in []pt) []pt {
	var out []pt
	for _, s := range seasonsOf(in) {
		if len(s) < minSeason {
			continue
		}
		ys := make([]float64, len(s))
		for i, p := range s {
			ys[i] = p.y
		}
		m := median(ys)
		for _, p := range s {
			out = append(out, pt{t: p.t, y: p.y - m, e: p.e})
		}
	}
	return out
}

func toLS(p []pt) []lsPoint {
	if len(p) == 0 {
		return nil
	}
	t0 := p[0].t
	out := make([]lsPoint, len(p))
	var sw float64
	for i, x := range p {
		w := 1 / (x.e * x.e)
		out[i] = lsPoint{t: x.t - t0, y: x.y, w: w}
		sw += w
	}
	for i := range out {
		out[i].w /= sw
	}
	return out
}

// periodogram is the floating mean Lomb-Scargle power of p from lsMinFreq
// to lsGridMax cycles a day, in steps of lsStep over the span.
type periodogram struct {
	f0, df float64
	pow    []float64
	span   float64
}

func newPeriodogram(p []pt) *periodogram {
	span := spanOf(p)
	if len(p) < 10 || span <= 0 {
		return nil
	}
	df := lsStep / span
	n := int((lsGridMax - lsMinFreq) / df)
	return &periodogram{f0: lsMinFreq, df: df, pow: lsPower(toLS(p), lsMinFreq, df, n), span: span}
}

// top are the periods of the k highest peaks at least peakSep over the
// span apart in frequency.
func (pg *periodogram) top(k int) []float64 {
	idx := make([]int, len(pg.pow))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int {
		switch {
		case pg.pow[a] > pg.pow[b]:
			return -1
		case pg.pow[a] < pg.pow[b]:
			return 1
		}
		return a - b
	})
	var fs []float64
	for _, i := range idx {
		f := pg.f0 + float64(i)*pg.df
		ok := true
		for _, g := range fs {
			if math.Abs(f-g) <= peakSep/pg.span {
				ok = false
				break
			}
		}
		if ok {
			fs = append(fs, f)
			if len(fs) == k {
				break
			}
		}
	}
	out := make([]float64, len(fs))
	for i, f := range fs {
		out[i] = 1 / f
	}
	return out
}

func amongPeriods(p float64, list []float64) bool {
	for _, q := range list {
		if math.Abs(q/p-1) < 0.01 {
			return true
		}
	}
	return false
}

// powerNear is the highest Lomb-Scargle power of p within half a cycle
// over the span of frequency 1/period.
func powerNear(p []pt, period float64) float64 {
	span := spanOf(p)
	if len(p) < 10 || span <= 0 {
		return 0
	}
	half := 0.5 / span
	df := lsFineStep / span
	f0 := math.Max(1/period-half, 1e-6)
	n := int(2*half/df) + 1
	best := 0.0
	for _, v := range lsPower(toLS(p), f0, df, n) {
		best = math.Max(best, v)
	}
	return best
}

// fap is the false alarm probability of power z over frequencies up to
// lsMaxFreq, by Baluev's (2008) bound for the floating mean periodogram.
func fap(z float64, p []pt) float64 {
	n := float64(len(p))
	if n < 5 || !(z > 0) {
		return 1
	}
	if z >= 1 {
		return 0
	}
	var sw, st, stt float64
	for _, x := range p {
		w := 1 / (x.e * x.e)
		sw += w
		st += w * x.t
	}
	mt := st / sw
	for _, x := range p {
		w := 1 / (x.e * x.e)
		stt += w * (x.t - mt) * (x.t - mt)
	}
	teff := math.Sqrt(4 * math.Pi * stt / sw)
	nh, nk := n-1, n-3
	lg1, _ := math.Lgamma(nh / 2)
	lg2, _ := math.Lgamma((nh - 1) / 2)
	gam := math.Sqrt(2/nh) * math.Exp(lg1-lg2)
	ls := 0.5 * nk * math.Log1p(-z)
	lt := math.Log(lsMaxFreq*teff*gam) + 0.5*(nk-1)*math.Log1p(-z) + 0.5*math.Log(0.5*nh*z)
	if ls < -20 && lt < -20 {
		hi, lo := math.Max(ls, lt), math.Min(ls, lt)
		return math.Exp(hi + math.Log1p(math.Exp(lo-hi)))
	}
	return 1 - (1-math.Exp(ls))*math.Exp(-math.Exp(lt))
}

// sineFit is a weighted least squares fit of a mean and a sine at a fixed
// period: its semi-amplitude, the amplitude's error with the errors scaled
// up by the fit's reduced chi-square when above 1, and the cosine and sine
// terms with their covariance.
type sineAt struct {
	amp, ampErr float64
	c, s        float64
	cov         [2][2]float64
}

func fitSineAt(p []pt, period float64) (sineAt, bool) {
	var m [3][3]float64
	var v [3]float64
	for _, x := range p {
		w := 1 / (x.e * x.e)
		th := 2 * math.Pi * x.t / period
		r := [3]float64{1, math.Cos(th), math.Sin(th)}
		for i := range 3 {
			v[i] += w * r[i] * x.y
			for j := range 3 {
				m[i][j] += w * r[i] * r[j]
			}
		}
	}
	inv, ok := inv3(m)
	if !ok || len(p) < 4 {
		return sineAt{}, false
	}
	var c [3]float64
	for i := range 3 {
		for j := range 3 {
			c[i] += inv[i][j] * v[j]
		}
	}
	var chi float64
	for _, x := range p {
		th := 2 * math.Pi * x.t / period
		r := (x.y - c[0] - c[1]*math.Cos(th) - c[2]*math.Sin(th)) / x.e
		chi += r * r
	}
	s2 := math.Max(1, chi/float64(len(p)-3))
	out := sineAt{c: c[1], s: c[2], amp: math.Hypot(c[1], c[2])}
	out.cov = [2][2]float64{{inv[1][1] * s2, inv[1][2] * s2}, {inv[2][1] * s2, inv[2][2] * s2}}
	a2 := math.Max(out.amp*out.amp, 1e-12)
	out.ampErr = math.Sqrt((c[1]*c[1]*out.cov[0][0] + c[2]*c[2]*out.cov[1][1]) / a2)
	return out, true
}

func inv3(m [3][3]float64) ([3][3]float64, bool) {
	var r [3][3]float64
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if !(math.Abs(det) > 0) {
		return r, false
	}
	r[0][0] = (m[1][1]*m[2][2] - m[1][2]*m[2][1]) / det
	r[0][1] = (m[0][2]*m[2][1] - m[0][1]*m[2][2]) / det
	r[0][2] = (m[0][1]*m[1][2] - m[0][2]*m[1][1]) / det
	r[1][0] = (m[1][2]*m[2][0] - m[1][0]*m[2][2]) / det
	r[1][1] = (m[0][0]*m[2][2] - m[0][2]*m[2][0]) / det
	r[1][2] = (m[0][2]*m[1][0] - m[0][0]*m[1][2]) / det
	r[2][0] = (m[1][0]*m[2][1] - m[1][1]*m[2][0]) / det
	r[2][1] = (m[0][1]*m[2][0] - m[0][0]*m[2][1]) / det
	r[2][2] = (m[0][0]*m[1][1] - m[0][1]*m[1][0]) / det
	return r, true
}

// coherence is the reduced chi-square of the per-season sine terms at a
// period about their weighted mean, over seasons with at least cohSeason
// points; ok is false with fewer than two such seasons.
func coherence(p []pt, period float64) (float64, bool) {
	var fits []sineAt
	for _, s := range seasonsOf(p) {
		if len(s) < cohSeason {
			continue
		}
		if f, ok := fitSineAt(s, period); ok {
			fits = append(fits, f)
		}
	}
	if len(fits) < 2 {
		return 0, false
	}
	var wt [2][2]float64
	var wx [2]float64
	ws := make([][2][2]float64, len(fits))
	for i, f := range fits {
		w, ok := inv2(f.cov)
		if !ok {
			return 0, false
		}
		ws[i] = w
		for a := range 2 {
			for b := range 2 {
				wt[a][b] += w[a][b]
			}
			wx[a] += w[a][0]*f.c + w[a][1]*f.s
		}
	}
	it, ok := inv2(wt)
	if !ok {
		return 0, false
	}
	mu := [2]float64{it[0][0]*wx[0] + it[0][1]*wx[1], it[1][0]*wx[0] + it[1][1]*wx[1]}
	var chi float64
	for i, f := range fits {
		d := [2]float64{f.c - mu[0], f.s - mu[1]}
		w := ws[i]
		chi += d[0]*(w[0][0]*d[0]+w[0][1]*d[1]) + d[1]*(w[1][0]*d[0]+w[1][1]*d[1])
	}
	return chi / float64(2*(len(fits)-1)), true
}

func inv2(m [2][2]float64) ([2][2]float64, bool) {
	det := m[0][0]*m[1][1] - m[0][1]*m[1][0]
	if !(math.Abs(det) > 0) {
		return [2][2]float64{}, false
	}
	return [2][2]float64{{m[1][1] / det, -m[0][1] / det}, {-m[1][0] / det, m[0][0] / det}}, true
}
