package minima

import (
	"math"
	"slices"
)

// Timing is one timed minimum against the ephemeris: Cycle the cycle
// number E from the ephemeris's epoch, Second whether it is the second
// eclipse, OC the observed minus calculated time and Err its one sigma
// error, in days, and Day the time in days, for spans.
type Timing struct {
	Cycle  float64
	Second bool
	OC     float64
	Err    float64
	Day    float64
}

// Trend states.
const (
	StateFlag  = "flag"
	StateClear = "clear"
	StateNeed  = "need"
)

// Trend ids.
const (
	TrendEphemeris = "ephemeris"
	TrendPeriod    = "period"
	TrendOrbit     = "orbit"
	TrendThird     = "third"
)

// Model names, the richest the timings support.
const (
	ModelNone   = "none"
	ModelOffset = "offset"
	ModelLine   = "line"
	ModelCurve  = "curve"
)

// Test is one trend's outcome: State, and for a flag or clear the size
// it measured with its error and the chance of it by noise alone.
type Test struct {
	State string  `json:"state"`
	Value float64 `json:"value"`
	Err   float64 `json:"err"`
	Prob  float64 `json:"prob"`
	// Period is a found wave's period in days.
	Period float64 `json:"period,omitempty"`
	// Need says what is missing when State is StateNeed.
	Need string `json:"need,omitempty"`
}

// Fit is the adopted model of the timings, in days and cycles: the
// offset of each eclipse at the epoch, the change per cycle, the change
// of that per cycle, and the scale the errors were multiplied by.
type Fit struct {
	Model     string  `json:"model"`
	Primary   float64 `json:"primary"`
	Secondary float64 `json:"secondary"`
	Slope     float64 `json:"slope"`
	SlopeErr  float64 `json:"slope_err"`
	Curve     float64 `json:"curve"`
	CurveErr  float64 `json:"curve_err"`
	HasSec    bool    `json:"has_sec"`
	Scale     float64 `json:"scale"`
}

// At is the fitted offset at cycle e.
func (f Fit) At(e float64, second bool) float64 {
	a := f.Primary
	if second {
		a = f.Secondary
	}
	return a + f.Slope*e + f.Curve*e*e
}

// Analysis is what a star's timings say: which were used, the adopted
// fit and the four trends.
type Analysis struct {
	N         int     `json:"n"`
	Primary   int     `json:"primary"`
	Secondary int     `json:"secondary"`
	SpanDays  float64 `json:"span_days"`
	Seasons   int     `json:"seasons"`
	Used      []bool  `json:"used"`
	Fit       Fit     `json:"fit"`
	// Now is the fitted offset of the epoch's eclipse at RefCycle, the
	// whole cycle of the latest timing, with its error, and Rate the
	// fitted change per cycle there, with its error: the local linear
	// ephemeris the timings call for.
	Now      float64 `json:"now"`
	NowErr   float64 `json:"now_err"`
	RefCycle float64 `json:"ref_cycle"`
	Rate     float64 `json:"rate"`
	RateErr  float64 `json:"rate_err"`

	Ephemeris Test `json:"ephemeris"`
	Period    Test `json:"period"`
	Orbit     Test `json:"orbit"`
	Third     Test `json:"third"`
}

const (
	// Alpha is the chance level below which a trend is flagged.
	Alpha = 0.001
	// seasonGap is the gap in days that starts a new observing season.
	seasonGap = 120.0
	// outlierZ is how far, in robust spreads, a timing may sit from the
	// others of its eclipse and season before it is left out.
	outlierZ = 5.0
	// minOffsetN is the fewest timings, on minOffsetCycles cycles, to
	// test for an offset.
	minOffsetN      = 3
	minOffsetCycles = 2
	// minLineN and minLineDays bound the test for a drift.
	minLineN    = 4
	minLineDays = 60.0
	// minCurveN, minCurveDays and minCurveSeasons bound the test for a
	// changing period.
	minCurveN       = 6
	minCurveDays    = 365.0
	minCurveSeasons = 2
	// minOrbitEach and minOrbitDays bound the test of the two eclipses
	// drifting apart.
	minOrbitEach = 3
	minOrbitDays = 300.0
	// minWaveN and minWaveDays bound the search for a wave; waveCycles is
	// how many of its cycles the timings must span.
	minWaveN    = 15
	minWaveDays = 1096.0
	waveCycles  = 1.5
	waveMinDays = 100.0
	waveTrials  = 300
	waveSims    = 2000
	// minShiftDays is the smallest offset now worth an ephemeris update,
	// one minute.
	minShiftDays = 1.0 / 1440
	// residDOF is the fewest degrees of freedom left in the timings for
	// a richer model.
	residDOF = 2
)

type spec struct {
	prim, sec   bool
	slope       bool
	split       bool
	curve       bool
	scaleCycles float64
}

func (s spec) cols(t Timing) []float64 {
	var c []float64
	if s.prim {
		c = append(c, b2f(!t.Second))
	}
	if s.sec {
		c = append(c, b2f(t.Second))
	}
	e := t.Cycle / s.scaleCycles
	switch {
	case s.split:
		c = append(c, b2f(!t.Second)*e, b2f(t.Second)*e)
	case s.slope:
		c = append(c, e)
	}
	if s.curve {
		c = append(c, e*e)
	}
	return c
}

func (s spec) k() int {
	return len(s.cols(Timing{}))
}

type solved struct {
	spec
	beta []float64
	cov  [][]float64
	chi  float64
	rows int
	ok   bool
}

// solve fits s to the used timings, the user's own alone: the survey's
// errors are not trusted to anchor the fit.
func solve(ts []Timing, used []bool, s spec) solved {
	var rows [][]float64
	var y, sig []float64
	for i, t := range ts {
		if !used[i] {
			continue
		}
		rows = append(rows, s.cols(t))
		y = append(y, t.OC)
		sig = append(sig, t.Err)
	}
	if s.k() == 0 {
		var chi float64
		for i := range y {
			chi += (y[i] / sig[i]) * (y[i] / sig[i])
		}
		return solved{spec: s, chi: chi, rows: len(rows), ok: true}
	}
	beta, cov, chi, ok := LSQ(rows, y, sig)
	return solved{spec: s, beta: beta, cov: cov, chi: chi, rows: len(rows), ok: ok}
}

func (m solved) dof() int { return m.rows - m.k() }

func (m solved) red() float64 {
	return math.Max(1, m.chi/math.Max(1, float64(m.dof())))
}

// compare is the chance that rich improves on simple this much by noise,
// by an F test: it judges the improvement against the scatter left, so
// timing errors that are too small do not make noise look significant.
func compare(simple, rich solved) float64 {
	dk := float64(rich.k() - simple.k())
	if !simple.ok || !rich.ok || dk <= 0 || rich.dof() < 1 || !(rich.chi > 0) {
		return 1
	}
	return FProb((simple.chi-rich.chi)/dk/(rich.chi/float64(rich.dof())), dk, float64(rich.dof()))
}

// Analyze reads the trends in ts. Each trend is tested only when the
// timings are enough for it, and otherwise says what it needs.
func Analyze(ts []Timing) Analysis {
	a := Analysis{N: len(ts), Used: make([]bool, len(ts))}
	for i := range ts {
		a.Used[i] = ts[i].Err > 0 && finite(ts[i].OC)
	}
	dropOutliers(ts, a.Used)
	used := filter(ts, a.Used)
	a.Primary, a.Secondary = count(used, false), count(used, true)
	a.SpanDays, a.Seasons = spanOf(used)
	scale := 1.0
	for _, t := range used {
		scale = math.Max(scale, math.Abs(t.Cycle))
	}
	hasP, hasS := a.Primary > 0, a.Secondary > 0
	base := spec{prim: hasP, sec: hasS, scaleCycles: scale}
	none := solve(ts, a.Used, spec{scaleCycles: scale})
	off := solve(ts, a.Used, base)
	line := base
	line.slope = true
	ln := solve(ts, a.Used, line)
	curve := line
	curve.curve = true
	cv := solve(ts, a.Used, curve)
	n := len(used)
	cycles := distinctCycles(used)

	best := none
	a.Ephemeris = Test{State: StateNeed, Need: needText(n, minOffsetN, "timed eclipses", cycles, minOffsetCycles)}
	canOffset := n >= minOffsetN && cycles >= minOffsetCycles && off.dof() >= 1
	canLine := canOffset && n >= minLineN && a.SpanDays >= minLineDays && ln.dof() >= residDOF
	canCurve := canLine && n >= minCurveN && a.SpanDays >= minCurveDays && a.Seasons >= minCurveSeasons && cv.dof() >= residDOF
	if canOffset {
		if compare(none, off) < Alpha {
			best = off
		}
		if canLine && compare(best, ln) < Alpha {
			best = ln
		}
	}
	shift := best
	a.Period = Test{State: StateNeed, Need: curveNeed(n, a.SpanDays, a.Seasons)}
	if canCurve {
		p := compare(ln, cv)
		c, ce := coef(cv, cv.k()-1, scale*scale)
		a.Period = Test{State: StateClear, Value: c, Err: ce, Prob: p}
		if p < Alpha {
			best = cv
			a.Period.State = StateFlag
		}
	}
	a.Fit = fitOf(best, scale, hasS)
	a.Fit.Scale = math.Sqrt(best.red())
	if n > 0 {
		last := used[0]
		for _, t := range used {
			if t.Day > last.Day {
				last = t
			}
		}
		a.RefCycle = math.Round(last.Cycle)
		a.Now, a.NowErr = local(best, a.RefCycle, false)
		a.Rate, a.RateErr = local(best, a.RefCycle, true)
		if a.NowErr == 0 {
			a.NowErr = last.Err
		}
	}
	if canOffset {
		p := compare(none, pickRicher(off, shift))
		a.Ephemeris = Test{State: StateClear, Value: a.Now, Err: a.NowErr, Prob: p}
		if shift.k() > 0 && p < Alpha && math.Abs(a.Now) >= minShiftDays {
			a.Ephemeris.State = StateFlag
		}
	}
	a.Orbit = orbit(ts, a.Used, used, best, scale)
	a.Third = third(ts, a.Used, used, best, a.SpanDays)
	return a
}

func pickRicher(a, b solved) solved {
	if b.k() > a.k() {
		return b
	}
	return a
}

// local is the fitted offset of the epoch's eclipse at cycle e, or with
// rate its change per cycle there, with its error from the fit's
// covariance scaled by its reduced chi-square.
func local(m solved, e float64, rate bool) (float64, float64) {
	if m.k() == 0 || !m.ok {
		return 0, 0
	}
	g := make([]float64, m.k())
	i := 0
	if m.prim {
		if !rate {
			g[i] = 1
		}
		i++
	}
	if m.sec {
		if !rate && !m.prim {
			g[i] = 1
		}
		i++
	}
	x := e / m.scaleCycles
	if m.slope {
		if rate {
			g[i] = 1 / m.scaleCycles
		} else {
			g[i] = x
		}
		i++
	}
	if m.curve {
		if rate {
			g[i] = 2 * x / m.scaleCycles
		} else {
			g[i] = x * x
		}
	}
	var v, val float64
	for p := range g {
		val += g[p] * m.beta[p]
		for q := range g {
			v += g[p] * m.cov[p][q] * g[q]
		}
	}
	return val, math.Sqrt(v * m.red())
}

// orbit tests whether the two eclipses drift apart: separate drifts for
// each against one shared.
func orbit(ts []Timing, use []bool, used []Timing, best solved, scale float64) Test {
	var p, s []Timing
	for _, t := range used {
		if t.Second {
			s = append(s, t)
		} else {
			p = append(p, t)
		}
	}
	sp, _ := spanOf(p)
	ss, _ := spanOf(s)
	if len(p) < minOrbitEach || len(s) < minOrbitEach || sp < minOrbitDays || ss < minOrbitDays {
		need := "Needs at least 3 timings of each eclipse, each spread over 300 days or more; has " +
			countOver(len(p), "of the deeper eclipse", sp) + " and " + countOver(len(s), "of the second", ss) + "."
		return Test{State: StateNeed, Need: need}
	}
	shared := spec{prim: true, sec: true, slope: true, curve: best.curve, scaleCycles: scale}
	apart := shared
	apart.split = true
	a, b := solve(ts, use, shared), solve(ts, use, apart)
	if !a.ok || !b.ok || b.dof() < residDOF {
		return Test{State: StateNeed, Need: "Needs more timings than the fit has terms."}
	}
	prob := compare(a, b)
	i, j := 2, 3
	diff := (b.beta[i] - b.beta[j]) / scale
	v := (b.cov[i][i] + b.cov[j][j] - 2*b.cov[i][j]) * b.red() / (scale * scale)
	t := Test{State: StateClear, Value: diff, Err: math.Sqrt(v), Prob: prob}
	if prob < Alpha {
		t.State = StateFlag
	}
	return t
}

// third looks for a wave in what the adopted fit leaves: at each trial
// period a sine's chi-square drop over the chi-square it leaves, a
// measure that does not depend on how big the timing errors are, its
// best judged against the best that noise alone gives the same timings
// under the same fit.
func third(ts []Timing, use []bool, used []Timing, best solved, span float64) Test {
	if len(used) < minWaveN || span < minWaveDays {
		return Test{State: StateNeed, Need: "Needs at least 15 timings over 3 years or more; has " + plural(len(used), "timing") + " over " + daysText(span) + "."}
	}
	res, sig := residuals(ts, use, best)
	days := make([]float64, 0, len(used))
	for _, t := range used {
		days = append(days, t.Day)
	}
	periods := wavePeriods(span)
	dof := float64(len(used) - best.k() - 2)
	if dof < 1 {
		return Test{State: StateNeed, Need: "Needs more timings than the fit has terms."}
	}
	obs, per, amp, ampErr := bestWave(days, res, sig, periods, dof)
	r := &rng{s: uint64(len(used))*2654435761 + 1}
	exceed := 0
	sim := make([]Timing, len(ts))
	copy(sim, ts)
	for range waveSims {
		for i := range sim {
			sim[i].OC = fitted(best, ts[i]) + ts[i].Err*r.normal()
		}
		sb := solve(sim, use, best.spec)
		if !sb.ok && best.k() > 0 {
			continue
		}
		sr, ss := residuals(sim, use, sb)
		if v, _, _, _ := bestWave(days, sr, ss, periods, dof); v >= obs {
			exceed++
		}
	}
	prob := float64(exceed+1) / float64(waveSims+1)
	t := Test{State: StateClear, Value: amp, Err: ampErr, Prob: prob, Period: per}
	if prob < Alpha && amp > 3*ampErr {
		t.State = StateFlag
	}
	return t
}

func wavePeriods(span float64) []float64 {
	hi := span / waveCycles
	lo := waveMinDays
	out := make([]float64, 0, waveTrials)
	for i := range waveTrials {
		out = append(out, lo*math.Pow(hi/lo, float64(i)/float64(waveTrials-1)))
	}
	return out
}

func residuals(ts []Timing, use []bool, m solved) ([]float64, []float64) {
	var res, sig []float64
	for i, t := range ts {
		if !use[i] {
			continue
		}
		res = append(res, t.OC-fitted(m, t))
		sig = append(sig, t.Err)
	}
	return res, sig
}

func fitted(m solved, t Timing) float64 {
	if m.k() == 0 || !m.ok {
		return 0
	}
	var v float64
	for j, c := range m.cols(t) {
		v += c * m.beta[j]
	}
	return v
}

// bestWave is the largest power a sine of any trial period gives the
// residuals, its chi-square drop over the chi-square left per degree of
// freedom, with that period, its amplitude and error.
func bestWave(days, res, sig, periods []float64, dof float64) (float64, float64, float64, float64) {
	var chi0 float64
	for i := range res {
		chi0 += (res[i] / sig[i]) * (res[i] / sig[i])
	}
	bestF, bestP, bestA, bestE := 0.0, 0.0, 0.0, 0.0
	rows := make([][]float64, len(res))
	for _, p := range periods {
		for i, d := range days {
			w := 2 * math.Pi * d / p
			rows[i] = []float64{math.Sin(w), math.Cos(w)}
		}
		beta, cov, chi, ok := LSQ(rows, res, sig)
		if !ok || !(chi > 0) {
			continue
		}
		red := chi / dof
		if f := (chi0 - chi) / red; f > bestF {
			amp := math.Hypot(beta[0], beta[1])
			var v float64
			if amp > 0 {
				g0, g1 := beta[0]/amp, beta[1]/amp
				v = (g0*g0*cov[0][0] + g1*g1*cov[1][1] + 2*g0*g1*cov[0][1]) * math.Max(1, red)
			}
			bestF, bestP, bestA, bestE = f, p, amp, math.Sqrt(v)
		}
	}
	return bestF, bestP, bestA, bestE
}

func fitOf(m solved, scale float64, hasS bool) Fit {
	f := Fit{Model: ModelNone, HasSec: hasS}
	if m.k() == 0 || !m.ok {
		return f
	}
	i := 0
	if m.prim {
		f.Primary = m.beta[i]
		i++
	}
	if m.sec {
		f.Secondary = m.beta[i]
		i++
	}
	if !m.prim {
		f.Primary = f.Secondary
	}
	if !m.sec {
		f.Secondary = f.Primary
	}
	f.Model = ModelOffset
	if m.slope {
		f.Slope, f.SlopeErr = coef(m, i, scale)
		f.Model = ModelLine
		i++
	}
	if m.curve {
		f.Curve, f.CurveErr = coef(m, i, scale*scale)
		f.Model = ModelCurve
	}
	return f
}

func coef(m solved, i int, scale float64) (float64, float64) {
	if !m.ok || i >= len(m.beta) {
		return 0, 0
	}
	return m.beta[i] / scale, math.Sqrt(m.cov[i][i]*m.red()) / scale
}

// dropOutliers leaves out timings far from the others of their eclipse
// in the same season, by the median and robust spread of their offsets,
// when the group has five or more; at most a fifth of the group. A
// timing alone in its season is never left out, as a real drift shows
// there first.
func dropOutliers(ts []Timing, used []bool) {
	type key struct {
		second bool
		season int
	}
	seasons := seasonIndex(ts, used)
	groups := map[key][]int{}
	for i, t := range ts {
		if used[i] {
			k := key{t.Second, seasons[i]}
			groups[k] = append(groups[k], i)
		}
	}
	for _, idx := range groups {
		if len(idx) < 5 {
			continue
		}
		z := make([]float64, len(idx))
		for k, i := range idx {
			z[k] = ts[i].OC
		}
		m := median(z)
		dev := make([]float64, len(z))
		for k, v := range z {
			dev[k] = math.Abs(v - m)
		}
		spread := 1.4826 * median(dev)
		type cand struct {
			i int
			z float64
		}
		var out []cand
		for k, i := range idx {
			s := math.Max(spread, ts[i].Err)
			if d := dev[k] / s; d > outlierZ && dev[k] > outlierZ*ts[i].Err {
				out = append(out, cand{i, d})
			}
		}
		slices.SortFunc(out, func(a, b cand) int { return cmpF(b.z, a.z) })
		for k, c := range out {
			if k >= len(idx)/5 {
				break
			}
			used[c.i] = false
		}
	}
}

// seasonIndex numbers the seasons of the used timings in time order.
func seasonIndex(ts []Timing, used []bool) []int {
	var order []int
	for i := range ts {
		if used[i] {
			order = append(order, i)
		}
	}
	slices.SortFunc(order, func(a, b int) int { return cmpF(ts[a].Day, ts[b].Day) })
	out := make([]int, len(ts))
	s := 0
	for k, i := range order {
		if k > 0 && ts[i].Day-ts[order[k-1]].Day > seasonGap {
			s++
		}
		out[i] = s
	}
	return out
}

func filter(ts []Timing, used []bool) []Timing {
	var out []Timing
	for i, t := range ts {
		if used[i] {
			out = append(out, t)
		}
	}
	return out
}

func count(ts []Timing, second bool) int {
	n := 0
	for _, t := range ts {
		if t.Second == second {
			n++
		}
	}
	return n
}

func distinctCycles(ts []Timing) int {
	seen := map[float64]bool{}
	for _, t := range ts {
		seen[math.Round(t.Cycle)] = true
	}
	return len(seen)
}

// spanOf is the days from the first timing to the last, and how many
// seasons they fall in, a gap of seasonGap days starting a new one.
func spanOf(ts []Timing) (float64, int) {
	if len(ts) == 0 {
		return 0, 0
	}
	days := make([]float64, len(ts))
	for i, t := range ts {
		days[i] = t.Day
	}
	slices.Sort(days)
	seasons := 1
	for i := 1; i < len(days); i++ {
		if days[i]-days[i-1] > seasonGap {
			seasons++
		}
	}
	return days[len(days)-1] - days[0], seasons
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
