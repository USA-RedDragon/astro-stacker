package period

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	minGain  = 100.0
	minDepZ  = 8.0
	modelGap = 25.0
)

type halfNote struct {
	doubled bool
	from    float64
	reason  string
	dark    darkTest
}

// reasonDark is the halfNote reason of two equal eclipses taken because
// no dark companion can make the single dip.
const reasonDark = "dark"

type analysis struct {
	d         *data
	f         *flatPts
	opts      Options
	ecl       *ecFit
	sine      *sineFit
	half      halfNote
	flatScore float64
	flatOff   []float64
	eph       *Ephemeris
	kind      Kind
	own       []lc.Series
	ztf       []lc.Series
	bandCache []*band
	bandsDone bool
	rob       *Robust
	fades     *Fades
	steadyAt  []float64
	steadyRun bool
	comps     []map[string][]compPt
	compsRead bool
	tessComps []TESSComp
}

func analyzeSeries(d *data, own, ztf []lc.Series, opts Options, at *Ephemeris) (*Result, error) {
	if opts.MinPeriod <= 0 {
		opts.MinPeriod = 0.2
	}
	if opts.MaxPeriod <= 0 {
		opts.MaxPeriod = 60
	}
	a := &analysis{d: d, opts: opts, own: own, ztf: ztf}
	res := &Result{Fits: []SurveyFit{}}
	if len(d.series) == 0 {
		res.Checks = noDataChecks()
		return res, ErrNoPeriod
	}
	a.f = flatten(d)
	a.flatOff, a.flatScore = flatFit(a.f)
	span := d.t1 - d.t0
	maxP := math.Min(opts.MaxPeriod, span/2)
	switch {
	case at != nil && at.Period > 0:
		a.fitAt(at)
	case maxP > opts.MinPeriod && span > 2:
		a.search(opts.MinPeriod, maxP)
	}
	a.settle()
	res.Fits = a.surveyFits()
	if (a.ecl != nil || a.sine != nil) && !a.supported() {
		a.ecl, a.sine = nil, nil
		a.settle()
		res.Fits = a.surveyFits()
	}
	res.Ephemeris = a.eph
	a.alignSrc()
	if a.kind == KindEclipsing || a.kind == KindSinusoidal {
		var comps [][]lc.Series
		if opts.Comparisons != nil {
			comps = opts.Comparisons()
		}
		a.rob = a.robust(comps)
		res.Robust = a.rob
	}
	res.Trend = a.trend()
	res.Checks = a.checks(res.Fits)
	res.Fades = a.fades
	res.SteadyFadeNights = a.steadyAt
	if a.kind == KindFlat || a.kind == KindIrregular {
		return res, ErrNoPeriod
	}
	return res, nil
}

// alignSrc moves each light curve an input series merges onto its
// largest one's zero point by the offsets the period model fitted with
// it, so a light curve that mostly catches eclipses is not lifted to its
// own median; the tests for false periods then read them aligned.
func (a *analysis) alignSrc() {
	var off []float64
	var keep []bool
	switch {
	case a.ecl != nil:
		off, keep = a.ecl.offset, a.ecl.keep
	case a.sine != nil:
		off, keep = a.sine.offset, a.sine.keep
	default:
		return
	}
	fitted := make([]bool, len(off))
	for i, k := range keep {
		fitted[a.f.s[i]] = fitted[a.f.s[i]] || k
	}
	for _, se := range a.d.series {
		if se.lead != se.idx && fitted[se.idx] && fitted[se.lead] {
			se.shift = off[se.idx] - off[se.lead]
		}
	}
	a.bandCache, a.bandsDone = nil, false
}

// fitAt fits the model of a given ephemeris, its period and epoch free
// to move a little.
func (a *analysis) fitAt(e *Ephemeris) {
	switch e.Kind {
	case KindEclipsing:
		dips := make([]dipP, len(e.Eclipses))
		for i, ec := range e.Eclipses {
			dips[i] = dipP{ec.Phase, ec.Width, ec.Shape}
		}
		if len(dips) > 0 {
			a.ecl = fitEclipse(a.f, a.d, ecInit{P: e.Period, T0: e.T0 - a.f.tref, dips: dips, freeP: true})
		}
	case KindSinusoidal:
		a.sine = fitSine(a.f, e.Period, true)
	case KindFlat, KindIrregular:
	}
}

func (a *analysis) settle() {
	switch {
	case a.ecl != nil:
		a.kind = KindEclipsing
		a.eph = a.eclipseEphemeris()
	case a.sine != nil:
		a.kind = KindSinusoidal
		a.eph = a.sineEphemeris()
	default:
		a.kind = a.noPeriodKind()
		a.eph = &Ephemeris{Kind: a.kind, Eclipses: []Eclipse{}}
	}
}

// supported says a survey other than NEOWISE, whose few visits a year
// cannot show a period alone, sees the found cycle on its own.
func (a *analysis) supported() bool {
	for g, gr := range a.d.groups {
		if gr.survey == lc.NEOWISE {
			continue
		}
		switch {
		case a.ecl != nil:
			for k := range a.ecl.dips {
				if a.ecl.active[g][k] && a.ecl.depth[g][k] >= 3*a.ecl.depthSigma(g, k) {
					return true
				}
			}
		case a.sine != nil:
			if a.sine.amp[g] > 3*a.sine.res.sigma(2+g) {
				return true
			}
		}
	}
	return false
}

func (a *analysis) nPoints() float64 { return float64(len(a.f.t)) }

func (a *analysis) search(minP, maxP float64) {
	cands := dipSearch(a.d, minP, maxP)
	var best *ecFit
	var bestNote halfNote
	bestBIC := math.Inf(1)
	var tried []float64
	for _, c := range cands {
		if len(tried) >= 3 {
			break
		}
		seen := false
		for _, p := range tried {
			for _, k := range []float64{1, 2} {
				if math.Abs(c.period*k-p)/p < 1e-3 {
					seen = true
				}
			}
		}
		if seen {
			continue
		}
		m, note := a.eclipseCandidate(c)
		if m == nil {
			continue
		}
		tried = append(tried, m.P)
		bic := m.robust + float64(m.freeCount())*math.Log(a.nPoints())
		if bic < bestBIC {
			best, bestNote, bestBIC = m, note, bic
		}
	}
	if best != nil && !a.eclipseSignificant(best) {
		best = nil
	}
	var sBest *sineFit
	sBIC := math.Inf(1)
	for _, p := range lsSearch(a.d, minP, maxP) {
		s := fitSine(a.f, p, true)
		bic := s.robust + float64(s.freeCount())*math.Log(a.nPoints())
		if bic < sBIC {
			sBest, sBIC = s, bic
		}
	}
	if sBest != nil && !a.sineSignificant(sBest) {
		sBest = nil
	}
	switch {
	case best != nil && (sBest == nil || bestBIC <= sBIC):
		a.ecl, a.half = a.timingRefit(best), bestNote
	case sBest != nil:
		a.sine = sBest
	}
}

func (a *analysis) initT0(period, phase float64) float64 {
	ph := frac(phase - a.f.tref/period)
	mid := (a.d.t0+a.d.t1)/2 - a.f.tref
	return period * (ph + math.Round(mid/period-ph))
}

func (a *analysis) eclipseCandidate(c candidate) (*ecFit, halfNote) {
	P := c.period
	w := math.Min(math.Max(0.5*c.width/P, 0.002), 0.25)
	t0 := a.initT0(P, c.phase)
	m := fitEclipse(a.f, a.d, ecInit{P: P, T0: t0, dips: []dipP{{0, w, 1}}, freeP: true})
	one := m
	if ph, sw, ok := a.findSecondary(m); ok {
		m2 := fitEclipse(a.f, a.d, ecInit{P: m.P, T0: m.T0, dips: []dipP{m.dips[0], {ph, sw, 1}}, freeP: true})
		if m2.robust < m.robust-modelGap && a.secondaryReal(m2) {
			m = m2
		}
	}
	if 2*one.P <= a.opts.MaxPeriod && a.offCentre(m) {
		if q := a.unlikeHalves(m); q != nil && q.robust < m.robust-modelGap && a.secondaryReal(q) {
			if why := a.dipsDiffer(q); why != "" {
				return a.orderDips(q), halfNote{doubled: true, from: m.P, reason: why}
			}
		}
	}
	if 2*one.P <= a.opts.MaxPeriod && !a.offCentre(m) {
		dw := one.dips[0].width / 2
		b := fitEclipse(a.f, a.d, ecInit{P: 2 * one.P, T0: one.T0, dips: []dipP{{0, dw, one.dips[0].shape}, {0.5, dw, one.dips[0].shape}}, freeP: true})
		if b.robust < one.robust-modelGap && a.secondaryReal(b) {
			if why := a.dipsDiffer(b); why != "" {
				return a.orderDips(b), halfNote{doubled: true, from: one.P, reason: why}
			}
		}
		if len(m.dips) == 1 && b.robust <= one.robust+modelGap && a.secondaryReal(b) && a.dipsDiffer(b) == "" {
			if d := a.darkCompanion(one); d.decided {
				return a.orderDips(b), halfNote{doubled: true, from: one.P, reason: reasonDark, dark: d}
			}
		}
	}
	return a.orderDips(m), halfNote{}
}

// offCentre says a fit's second dip sits clearly away from phase 0.5. Such
// a pair rules out twice the period: at 2P its dips would fall at 0, about
// 0.5 and the two phases between, so both halves of the cycle would be
// alike whatever the depths.
func (a *analysis) offCentre(m *ecFit) bool {
	if len(m.dips) < 2 || !a.secondaryReal(m) {
		return false
	}
	dp := math.Abs(m.dips[1].phase - 0.5)
	return dp > 0.002 && dp/m.res.sigma(m.iPh[1]) >= 5
}

// unlikeHalves fits a two dip model's four dips at twice its period, each
// with its own depth. When both halves of that cycle are alike the period
// stands and it returns nil. When each dip shows in only one half, the
// period is half the true one and it returns the two dip fit at twice the
// period with the dips that show.
func (a *analysis) unlikeHalves(m *ecFit) *ecFit {
	d0, d1 := m.dips[0], m.dips[1]
	ph := d1.phase / 2
	q := fitEclipse(a.f, a.d, ecInit{P: 2 * m.P, T0: m.T0, freeP: true, dips: []dipP{
		{0, d0.width / 2, d0.shape}, {ph, d1.width / 2, d1.shape}, {0.5, d0.width / 2, d0.shape}, {0.5 + ph, d1.width / 2, d1.shape}}})
	var keep []dipP
	for k := range 2 {
		on, ok := a.oneHalf(q, k, k+2)
		if !ok {
			return nil
		}
		keep = append(keep, q.dips[on])
	}
	if keep[0].phase > keep[1].phase {
		keep[0], keep[1] = keep[1], keep[0]
	}
	t0 := q.T0 + keep[0].phase*q.P
	keep[1].phase = frac(keep[1].phase - keep[0].phase)
	keep[0].phase = 0
	return fitEclipse(a.f, a.d, ecInit{P: q.P, T0: t0, freeP: true, dips: keep})
}

// oneHalf says which of dips j and k, the same dip in the two halves of a
// cycle, shows when the other is under half its depth by at least five
// sigma in some band.
func (a *analysis) oneHalf(q *ecFit, j, k int) (int, bool) {
	for g := range q.depth {
		if !q.active[g][j] || !q.active[g][k] {
			continue
		}
		hi, lo, on := q.depth[g][j], q.depth[g][k], j
		if lo > hi {
			hi, lo, on = lo, hi, k
		}
		if hi >= 0.01 && lo < hi/2 && (hi-lo) >= 5*math.Hypot(q.depthSigma(g, j), q.depthSigma(g, k)) {
			return on, true
		}
	}
	return 0, false
}

func (a *analysis) secondaryReal(m *ecFit) bool {
	for g := range m.depth {
		if m.active[g][1] && m.depth[g][1] >= 0.01 && m.depth[g][1] >= 5*m.depthSigma(g, 1) {
			return true
		}
	}
	return false
}

func timingSurvey(s string) bool { return s == lc.ZTF || s == lc.TESS }

func (a *analysis) timingRefit(m *ecFit) *ecFit {
	mask := make([]bool, len(a.f.t))
	all := true
	for i := range a.f.t {
		mask[i] = timingSurvey(a.d.groups[a.f.g[i]].survey)
		all = all && mask[i]
	}
	if all || !a.timingCovered(m, mask) {
		return m
	}
	in := ecInit{P: m.P, T0: m.T0, dips: append([]dipP(nil), m.dips...), freeP: true, mask: mask}
	t := fitEclipse(a.f, a.d, in)
	if !a.timingCovered(t, mask) {
		return m
	}
	in = ecInit{P: t.P, T0: t.T0, dips: append([]dipP(nil), t.dips...), freeP: true, fixed: true}
	out := fitEclipse(a.f, a.d, in)
	shared := []int{0, 1}
	for k := range out.dips {
		if k > 0 {
			shared = append(shared, out.iPh[k])
		}
		shared = append(shared, out.iW[k], out.iA[k])
	}
	for _, i := range shared {
		for _, j := range shared {
			out.res.cov[i][j] = t.res.cov[i][j]
		}
	}
	out.timing = true
	return out
}

func (a *analysis) timingCovered(m *ecFit, mask []bool) bool {
	for k := range m.dips {
		cycles := map[int]bool{}
		n := 0
		for i := range a.f.t {
			if !mask[i] {
				continue
			}
			if sh, u := m.shapeAt(a.f.t[i], k); sh > 0.5 {
				cycles[int(math.Round((a.f.t[i]-m.T0)/m.P-u))] = true
				n++
			}
		}
		if len(cycles) < 3 || n < 10 {
			return false
		}
	}
	return true
}

func (a *analysis) orderDips(m *ecFit) *ecFit {
	if len(m.dips) < 2 {
		return m
	}
	d0, d1 := a.meanDepth(m, 0), a.meanDepth(m, 1)
	if d1 <= d0 {
		return m
	}
	ph := m.dips[1].phase
	in := ecInit{P: m.P, T0: m.T0 + ph*m.P, freeP: true,
		dips: []dipP{{0, m.dips[1].width, m.dips[1].shape}, {frac(1 - ph), m.dips[0].width, m.dips[0].shape}}}
	return fitEclipse(a.f, a.d, in)
}

func (a *analysis) meanDepth(m *ecFit, k int) float64 {
	var s, n float64
	for g := range m.depth {
		if m.active[g][k] {
			s += undilute(m.depth[g][k], a.d.groups[g].dil)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return s / n
}

func (a *analysis) findSecondary(m *ecFit) (float64, float64, bool) {
	f := a.f
	r := make([]float64, len(f.t))
	ph := make([]float64, len(f.t))
	for i := range f.t {
		r[i] = f.y[i] - m.value(f.t[i], f.g[i], f.s[i])
		ph[i] = frac((f.t[i] - m.T0) / m.P)
	}
	w0 := m.dips[0].width
	bestSNR, bestPh, bestW := 0.0, 0.0, 0.0
	for _, hw := range []float64{w0 / 2, w0, 2 * w0} {
		step := hw / 4
		for c := w0 + hw; c < 1-w0-hw; c += step {
			var sw, swr float64
			cycles := map[int]bool{}
			for i := range f.t {
				if math.Abs(wrapPhase(ph[i]-c)) >= hw {
					continue
				}
				w := 1 / (f.e[i] * f.e[i])
				sw += w
				swr += w * r[i]
				cycles[int(math.Floor((f.t[i]-m.T0)/m.P-c+0.5))] = true
			}
			if sw == 0 || len(cycles) < 3 {
				continue
			}
			mean := swr / sw
			snr := mean * math.Sqrt(sw)
			if mean > 0.01 && snr > bestSNR {
				bestSNR, bestPh, bestW = snr, c, hw
			}
		}
	}
	return bestPh, math.Max(bestW, 0.002), bestSNR >= 8
}

func (a *analysis) dipsDiffer(m *ecFit) string {
	if len(m.dips) < 2 {
		return ""
	}
	g, z := a.depthDiff(m)
	if z >= 5 && g >= 0 {
		return "depth"
	}
	if a.offCentre(m) {
		return "phase"
	}
	wz := math.Abs(m.dips[0].width-m.dips[1].width) / math.Hypot(m.res.sigma(m.iW[0]), m.res.sigma(m.iW[1]))
	if wz >= 5 && math.Abs(m.dips[0].width-m.dips[1].width) > 0.1*math.Max(m.dips[0].width, m.dips[1].width) {
		return "width"
	}
	return ""
}

func (a *analysis) depthDiff(m *ecFit) (int, float64) {
	bestG, bestZ := -1, 0.0
	for g := range m.depth {
		if z := a.depthZ(m, g); z > bestZ {
			bestG, bestZ = g, z
		}
	}
	return bestG, bestZ
}

func (a *analysis) depthZ(m *ecFit, g int) float64 {
	if len(m.dips) < 2 || !m.active[g][0] || !m.active[g][1] {
		return 0
	}
	d := math.Abs(m.depth[g][0] - m.depth[g][1])
	if d < 0.01 || d < 0.05*math.Max(m.depth[g][0], m.depth[g][1]) {
		return 0
	}
	return d / math.Hypot(m.depthSigma(g, 0), m.depthSigma(g, 1))
}

func (a *analysis) eclipseSignificant(m *ecFit) bool {
	gain := a.flatScore - m.robust - float64(m.freeCount()-len(m.offset))*math.Log(a.nPoints())
	if gain < minGain {
		return false
	}
	bestZ := 0.0
	for g := range m.depth {
		if m.active[g][0] {
			bestZ = math.Max(bestZ, m.depth[g][0]/m.depthSigma(g, 0))
		}
	}
	if bestZ < minDepZ {
		return false
	}
	cycles := map[int]bool{}
	for i := range a.f.t {
		if !m.keep[i] {
			continue
		}
		sh, u := m.shapeAt(a.f.t[i], 0)
		dd := m.depth[a.f.g[i]][0]
		if sh > 0.5 && a.f.y[i]-m.offset[a.f.s[i]] > 0.5*dd*sh && dd > 0 {
			cycles[int(math.Round((a.f.t[i]-m.T0)/m.P-u))] = true
		}
	}
	if len(cycles) < 3 {
		return false
	}
	if n, s := a.dipEpochs(m); n < dipNights || s < dipSeasons {
		return false
	}
	return a.boxHolds(m)
}

// boxHolds says a box shaped dip at one of m's dips stands out at boxMinZ
// on at least boxMinNights nights in a band with the slow changes removed
// and no night counting for more than a few points; true when no band has
// enough points to test.
func (a *analysis) boxHolds(m *ecFit) bool {
	tested := false
	where := a.dipWhere(m)
	for _, b := range a.bands() {
		tested = true
		bx := boxSearch(b.hp, m.P, b.weight, where)
		if bx.snr >= boxMinZ && boxNights(b.hp, m.P, bx) >= boxMinNights {
			return true
		}
	}
	return !tested
}

func (a *analysis) sineSignificant(s *sineFit) bool {
	gain := a.flatScore - s.robust - float64(1+len(s.amp))*math.Log(a.nPoints())
	if gain < minGain || s.ampZ() < minDepZ {
		return false
	}
	return gain >= 0.1*a.flatScore
}

func (a *analysis) noPeriodKind() Kind {
	var chi float64
	for i := range a.f.t {
		r := (a.f.y[i] - a.flatOff[a.f.s[i]]) / a.f.e[i]
		chi += math.Min(r*r, 100)
	}
	if n := a.nPoints(); n > 0 && chi/n > 4 {
		return KindIrregular
	}
	return KindFlat
}

func (a *analysis) eclipseEphemeris() *Ephemeris {
	m := a.ecl
	e := &Ephemeris{Period: m.P, PeriodErr: m.res.sigma(0), T0: m.T0 + a.f.tref, T0Err: m.res.sigma(1), Kind: KindEclipsing}
	for k, dp := range m.dips {
		ec := Eclipse{Phase: dp.phase, Width: dp.width, Shape: dp.shape, Depth: map[string]float64{}}
		for g, gr := range a.d.groups {
			if m.active[g][k] {
				ec.Depth[gr.key] = undilute(m.depth[g][k], gr.dil)
			}
		}
		e.Eclipses = append(e.Eclipses, ec)
	}
	return e
}

func (a *analysis) sineEphemeris() *Ephemeris {
	s := a.sine
	e := &Ephemeris{Period: s.P, PeriodErr: s.res.sigma(0), T0: s.T0 + a.f.tref, T0Err: s.res.sigma(1), Kind: KindSinusoidal,
		Eclipses: []Eclipse{}, Amplitude: map[string]float64{}}
	for g, gr := range a.d.groups {
		e.Amplitude[gr.key] = 2 * undilute(s.amp[g], gr.dil)
	}
	return e
}

func noDataChecks() []check.Check {
	why := "No survey light curve to search."
	return []check.Check{
		check.New(check.Period, check.NotRun, why),
		check.New(check.Fold, check.NotRun, why),
		check.New(check.Half, check.NotRun, why),
		check.New(check.Double, check.NotRun, why),
		check.New(check.ZTF, check.NotRun, "No ZTF points for this star."),
		check.New(check.TESS, check.NotRun, "No TESS points for this star."),
		check.New(check.ATLAS, check.NotRun, "No ATLAS points for this star."),
		check.New(check.NEOWISE, check.NotRun, "No NEOWISE points for this star."),
	}
}

func analyze(series []lc.Series, opts Options, at *Ephemeris) (*Result, error) {
	var own, ztf []lc.Series
	for _, s := range series {
		switch s.Survey {
		case lc.Own:
			own = append(own, s)
		case lc.ZTF:
			ztf = append(ztf, s)
		}
	}
	opts.Comparisons = once(opts.Comparisons)
	res, err := analyzeSeries(prepare(series), own, ztf, opts, at)
	if at != nil {
		return res, err
	}
	p, ok := partnerToPromote(res)
	if !ok {
		return res, err
	}
	alt, aerr := analyzeSeries(prepare(series), own, ztf, opts, &Ephemeris{Kind: KindSinusoidal, Period: p})
	if aerr != nil || !promotable(alt, p) {
		return res, err
	}
	alt.Robust.Alias, alt.Robust.AliasEcho = res.Ephemeris.Period, res.Robust.Echo
	alt.Robust.Real = true
	return alt, nil
}

// partnerToPromote is the partner cycle to test in place of a smooth
// period that sits next to an echo of the day, month or year that
// nothing confirms, when the partner is not such an echo itself.
func partnerToPromote(res *Result) (float64, bool) {
	if res == nil || res.Ephemeris == nil || res.Ephemeris.Kind != KindSinusoidal || res.Robust == nil {
		return 0, false
	}
	r := res.Robust
	if r.Echo == "" || r.Confirmed != "" || r.Partner <= 0 || echoOf(r.Partner, 0, true) != "" {
		return 0, false
	}
	return r.Partner, true
}

// promotable says the fit at partner period p holds where the echo did
// not: not an echo, at p, surviving the slow changes and above the
// comparison stars in promoteBands bands or more, and not refuted by a
// different cycle in TESS. Such a cycle is real even when the slow
// changes hide it in the raw points.
func promotable(alt *Result, p float64) bool {
	if alt == nil || alt.Ephemeris == nil || alt.Ephemeris.Kind != KindSinusoidal || alt.Robust == nil {
		return false
	}
	r := alt.Robust
	if r.Echo != "" || !r.Stable || !r.CompPass || r.Bands < promoteBands {
		return false
	}
	if math.Abs(alt.Ephemeris.Period/p-1) > 0.01 {
		return false
	}
	return r.TESSPeriod == 0 || math.Abs(r.TESSPeriod/alt.Ephemeris.Period-1) < 0.02
}

// promoteBands is how many survey bands must show a partner cycle before
// it replaces an echo.
const promoteBands = 2

func once(fn func() [][]lc.Series) func() [][]lc.Series {
	if fn == nil {
		return nil
	}
	var done bool
	var v [][]lc.Series
	return func() [][]lc.Series {
		if !done {
			v, done = fn(), true
		}
		return v
	}
}
