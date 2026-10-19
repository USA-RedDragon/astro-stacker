package minima

import "math"

const (
	// checkSims is how many nights are made up at the best fit to learn
	// how far the two methods' middles differ by chance alone, and
	// checkMin the fewest of them both methods must time for that.
	checkSims = 100
	checkMin  = 40
	// checkNear is how many widths either side of the fitted one the
	// made-up nights are fitted at, and checkSteps how many steps their
	// middle is looked for in.
	checkNear  = 2
	checkSteps = 40
	// agreeZ is how many times that chance spread the two methods'
	// difference may stray from its mean over the made-up nights: about
	// 1% of right timings stray further.
	agreeZ = 2.576
)

// Spread is how far the two methods' middles differ by chance at a fit:
// the mean and spread of the fit's middle less the second method's over
// nights made up at it.
type Spread struct {
	Mean float64 `json:"mean"`
	SD   float64 `json:"sd"`
	N    int     `json:"n"`
	// BeadSD is the spread of the difference over the fit's residuals
	// moved along the night.
	BeadSD float64 `json:"bead_sd"`
}

// agrees is whether the fit's middle less the second method's, diff,
// lies within agreeZ times the larger of s's two spreads of its mean:
// made-up noise misses what the night's own residuals carry, such as
// frames lost in the dip.
func agrees(diff float64, s Spread) bool {
	return math.Abs(diff-s.Mean) <= agreeZ*s.Sigma()
}

// Sigma is the larger of s's two spreads.
func (s Spread) Sigma() float64 { return math.Max(s.SD, s.BeadSD) }

// checkSpread is the chance spread of the fit's middle less the second
// method's. Each of checkSims nights is the best fit's model plus the
// fit's own residuals drawn at random, scaled to each point's error and
// widened by red, the drifting noise factor, and by the reduced
// chi-square when it is under 1; the dip is fitted again on it at the
// fitted width fd and the checkNear widths in widthScales of d either
// side of it, with the fit's straight line if it took one, as the night
// itself was, and the best width's middle is timed by the second method.
// ok is false when fewer than checkMin nights give both.
func checkSpread(pts []Point, ng int, d, fd Dip, span float64, slope bool, best fitOut, red float64) (Spread, bool) {
	n := len(pts)
	z := make([]float64, n)
	var ss float64
	for i, p := range pts {
		z[i] = best.resid[i] / p.E
		ss += z[i] * z[i]
	}
	scale := math.Max(1, red)
	if rms := math.Sqrt(ss / float64(n)); rms > 0 && rms < 1 {
		scale /= rms
	}
	ws := widthScales()
	at := 0
	for i, s := range ws {
		if math.Abs(s*d.Half-fd.Half) < math.Abs(ws[at]*d.Half-fd.Half) {
			at = i
		}
	}
	near := ws[max(0, at-checkNear):min(len(ws), at+checkNear+1)]
	dips := make([]Dip, len(near))
	for i, s := range near {
		dips[i] = scaled(d, s)
	}
	r := &rng{s: math.Float64bits(pts[0].T) ^ uint64(n)<<32 ^ math.Float64bits(pts[n-1].Y)}
	sim := make([]Point, n)
	var diffs []float64
	refit(ng, dips, span, slope, best, checkSims, checkSteps, func(int) []Point {
		for i, p := range pts {
			sim[i] = Point{T: p.T, Y: best.model[i] + scale*p.E*z[int(r.uniform()*float64(n))%n], E: p.E, G: p.G}
		}
		return sim
	}, func(f fitOut, fd Dip, ps []Point) {
		t := d.Mid + f.tau
		if c, ok := KvW(normalized(ps, f), t, fd.Half); ok {
			diffs = append(diffs, t-c.T)
		}
	})
	if len(diffs) < checkMin {
		return Spread{}, false
	}
	var mean float64
	for _, v := range diffs {
		mean += v
	}
	mean /= float64(len(diffs))
	return Spread{Mean: mean, SD: stddev(diffs), N: len(diffs)}, true
}

// refit fits the dip again at each of dips' widths, over beadReach
// formal errors either side of the best middle in steps, on count nights
// made by night, handing the best width's fit to got with that dip and
// the night's points; it says whether some night's best middle fell at
// the edge of that reach.
func refit(ng int, dips []Dip, span float64, slope bool, best fitOut, count, steps int, night func(j int) []Point, got func(fitOut, Dip, []Point)) bool {
	reach := math.Min(span, beadReach*best.formal)
	lo, hi := math.Max(-span, best.tau-reach), math.Min(span, best.tau+reach)
	edge := (hi - lo) / float64(steps)
	hit := false
	for j := range count {
		ps := night(j)
		var bf fitOut
		var bd Dip
		found := false
		for _, d := range dips {
			f, fail := fitShapeIn(ps, ng, d, lo, hi, steps, slope)
			if fail == fitDone && (!found || f.chi < bf.chi) {
				bf, bd, found = f, d, true
			}
		}
		if !found {
			continue
		}
		if bf.tau <= lo+edge || bf.tau >= hi-edge {
			hit = true
			continue
		}
		got(bf, bd, ps)
	}
	return hit
}
