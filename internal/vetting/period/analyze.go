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
}

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
}

func analyzeSeries(d *data, opts Options) (*Result, error) {
	if opts.MinPeriod <= 0 {
		opts.MinPeriod = 0.2
	}
	if opts.MaxPeriod <= 0 {
		opts.MaxPeriod = 60
	}
	a := &analysis{d: d, opts: opts}
	res := &Result{Fits: []SurveyFit{}}
	if len(d.series) == 0 {
		res.Checks = noDataChecks()
		return res, ErrNoPeriod
	}
	a.f = flatten(d)
	a.flatOff, a.flatScore = flatFit(a.f)
	span := d.t1 - d.t0
	maxP := math.Min(opts.MaxPeriod, span/2)
	if maxP > opts.MinPeriod && span > 2 {
		a.search(opts.MinPeriod, maxP)
	}
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
	res.Ephemeris = a.eph
	res.Fits = a.surveyFits()
	res.Checks = a.checks(res.Fits)
	if a.kind == KindFlat || a.kind == KindIrregular {
		return res, ErrNoPeriod
	}
	return res, nil
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
	if 2*m.P <= a.opts.MaxPeriod {
		dw := m.dips[0].width / 2
		b := fitEclipse(a.f, a.d, ecInit{P: 2 * m.P, T0: m.T0, dips: []dipP{{0, dw, m.dips[0].shape}, {0.5, dw, m.dips[0].shape}}, freeP: true})
		if b.robust < m.robust-modelGap && a.secondaryReal(b) {
			if why := a.dipsDiffer(b); why != "" {
				return a.orderDips(b), halfNote{doubled: true, from: m.P, reason: why}
			}
		}
	}
	if ph, sw, ok := a.findSecondary(m); ok {
		m2 := fitEclipse(a.f, a.d, ecInit{P: m.P, T0: m.T0, dips: []dipP{m.dips[0], {ph, sw, 1}}, freeP: true})
		if m2.robust < m.robust-modelGap && a.secondaryReal(m2) {
			m = m2
		}
	}
	return a.orderDips(m), halfNote{}
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
	sp := m.res.sigma(m.iPh[1])
	if dp := math.Abs(m.dips[1].phase - 0.5); dp > 0.002 && dp/sp >= 5 {
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
	return len(cycles) >= 3
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

func analyze(series []lc.Series, opts Options) (*Result, error) {
	return analyzeSeries(prepare(series), opts)
}
