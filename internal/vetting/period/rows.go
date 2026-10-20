package period

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func surveyID(s string) string {
	switch s {
	case lc.ZTF:
		return check.ZTF
	case lc.ATLAS:
		return check.ATLAS
	case lc.TESS:
		return check.TESS
	}
	return check.NEOWISE
}

type surveyInfo struct {
	groups []int
	counts string
	span   string
	t0, t1 float64
	dil    float64
	n      int
}

func (a *analysis) info(s string) surveyInfo {
	si := surveyInfo{t0: math.Inf(1), t1: math.Inf(-1), dil: 1}
	var parts []string
	for g, gr := range a.d.groups {
		if gr.survey != s {
			continue
		}
		si.groups = append(si.groups, g)
		si.n += gr.points
		si.dil = math.Min(si.dil, gr.dil)
		if gr.band == "" {
			parts = append(parts, commas(gr.points))
		} else {
			parts = append(parts, commas(gr.points)+" "+gr.band)
		}
		for _, se := range gr.series {
			r := a.d.series[se].raw
			si.t0 = math.Min(si.t0, r[0].t)
			si.t1 = math.Max(si.t1, r[len(r)-1].t)
		}
	}
	unit := "points"
	if si.n == 1 {
		unit = "point"
	}
	si.counts = joinList(parts) + " " + unit
	if len(si.groups) > 0 {
		si.span = yearSpan(si.t0, si.t1)
	}
	return si
}

func (a *analysis) surveyRow(s string, fits []SurveyFit) check.Check {
	id := surveyID(s)
	si := a.info(s)
	if len(si.groups) == 0 {
		return check.New(id, check.NotRun, fmt.Sprintf("No %s points for this star.", s))
	}
	switch a.kind {
	case KindEclipsing:
		if s == lc.TESS {
			r := a.tessRow(si, fits)
			if r.Status == check.Pass && slices.Contains(a.periodSurveys(), s) {
				r.Evidence += fmt.Sprintf(" Repeats every %s days.", fmtPeriod(a.ecl.P, a.ecl.res.sigma(0)))
			}
			return r
		}
		return a.eclipseRow(s, si, fits)
	case KindSinusoidal:
		return a.sineRow(s, si, fits)
	case KindFlat, KindIrregular:
	}
	return a.flatRow(s, si)
}

func blendNote(si surveyInfo) string {
	if si.dil < 0.95 {
		return " A neighbor blends in here, so the dips look shallower."
	}
	return ""
}

func (a *analysis) eclipseRow(s string, si surveyInfo, fits []SurveyFit) check.Check {
	id := surveyID(s)
	m := a.ecl
	base := fmt.Sprintf("%s %s.", si.counts, si.span)
	in, bad := 0, -1
	bestG, bestIn, bestShows, noisy := -1, 0, false, false
	for _, g := range si.groups {
		f := fits[g]
		in += f.InDip1 + f.InDip2
		if !f.Agrees && bad < 0 {
			bad = g
		}
		if !(m.active[g][0] || (len(m.dips) > 1 && m.active[g][1])) {
			continue
		}
		n := f.InDip1 + f.InDip2
		if (f.Shows && !bestShows) || (f.Shows == bestShows && n > bestIn) {
			bestG, bestIn, bestShows = g, n, f.Shows
		}
	}
	if bad >= 0 {
		return check.New(id, check.Fail, base+" "+a.shallowWords(bad)+", against the period from the other surveys.")
	}
	if in == 0 {
		return check.New(id, check.NotRun, base+" None fall in a dip.")
	}
	if bestG < 0 {
		return check.New(id, check.NotRun, fmt.Sprintf("%s %s fall near dips, but too few deep inside one to measure it.", base, plural(in, "point", "points")))
	}
	for _, g := range si.groups {
		noisy = noisy || a.coreDip(g)
	}
	if !bestShows && noisy {
		return check.New(id, check.NotRun, fmt.Sprintf("%s %s fall in dips, but they scatter too much to show the dip or rule it out.", base, plural(in, "point", "points")))
	}
	repeats := ""
	if slices.Contains(a.periodSurveys(), s) {
		repeats = fmt.Sprintf(" Repeats every %s days.", fmtPeriod(m.P, m.res.sigma(0)))
	}
	if s == lc.ZTF && repeats != "" {
		return check.New(id, check.Pass, base+repeats)
	}
	if s == lc.NEOWISE {
		if txt := a.neowiseCatch(); txt != "" {
			return check.New(id, check.Pass, txt+blendNote(si)+repeats)
		}
	}
	k := 0
	if !m.active[bestG][0] {
		k = 1
	}
	return check.New(id, check.Pass, fmt.Sprintf("%s %s fall in dips, %s %s mag deep in %s, as the period predicts.%s%s",
		base, plural(in, "point", "points"), a.dipName(k), mag2(m.depth[bestG][k]), bandLabel(a.d.groups[bestG]), blendNote(si), repeats))
}

// coreDip reports whether group g has three or more points deep inside
// an active dip, enough that its depth was measured.
func (a *analysis) coreDip(g int) bool {
	m := a.ecl
	n := 0
	a.rawPoints(func(se *series, p obs) {
		if se.group != g {
			return
		}
		for k := range m.dips {
			if sh, _ := m.shapeAt(p.t, k); sh > 0.5 && m.active[g][k] {
				n++
				return
			}
		}
	})
	return n >= 3
}

// shallowWords says how group g's dips fall short of the depth the other
// bands predict.
func (a *analysis) shallowWords(g int) string {
	m := a.ecl
	shown := m.shownDepths(a.d)
	gr := a.d.groups[g]
	for k := range m.dips {
		if !m.active[g][k] {
			continue
		}
		pred := predictedDepth(shown[k], g, gr.dil)
		if pred < 0 {
			continue
		}
		dep, se := m.depth[g][k], m.depthSigma(g, k)
		if contradicts(dep, se, pred) {
			return fmt.Sprintf("The points in %s %s are %s +- %s mag fainter, well short of the %s mag the other bands show", bandLabel(gr), a.dipName(k), mag2(dep), mag2(se), mag2(pred))
		}
	}
	return fmt.Sprintf("The points in the dips are not fainter in %s", bandLabel(gr))
}

// periodSurveys names the surveys whose points set the period: each
// whose own information on the period, with the epoch free and its
// errors scaled up by its scatter about the model, is at least a fifth
// of the most any survey gives. With the period from ZTF and TESS alone
// only those count.
func (a *analysis) periodSurveys() []string {
	m := a.ecl
	if m == nil {
		return nil
	}
	f := a.f
	type info struct{ pp, pt, tt, chi, n float64 }
	by := map[string]*info{}
	p := m.pack()
	eval := m.evalFn(f)
	idx := make([]int, m.nParams)
	der := make([]float64, m.nParams)
	for i := range f.t {
		if !m.keep[i] {
			continue
		}
		val, _ := eval(i, p, idx, der)
		s := a.d.groups[f.g[i]].survey
		x := by[s]
		if x == nil {
			x = &info{}
			by[s] = x
		}
		w := 1 / (f.e[i] * f.e[i])
		x.pp += w * der[0] * der[0]
		x.pt += w * der[0] * der[1]
		x.tt += w * der[1] * der[1]
		r := f.y[i] - val
		x.chi += w * r * r
		x.n++
	}
	marg := map[string]float64{}
	most := 0.0
	for s, x := range by {
		if x.n == 0 || x.tt <= 0 {
			continue
		}
		v := (x.pp - x.pt*x.pt/x.tt) / math.Max(1, x.chi/x.n)
		marg[s] = v
		most = math.Max(most, v)
	}
	var out []string
	if m.timing {
		for s := range marg {
			if s != lc.ZTF && s != lc.TESS {
				delete(marg, s)
			}
		}
		most = 0
		for _, v := range marg {
			most = math.Max(most, v)
		}
	}
	for _, s := range []string{lc.ZTF, lc.ATLAS, lc.TESS, lc.NEOWISE} {
		if v, ok := marg[s]; ok && most > 0 && v >= 0.2*most {
			out = append(out, s)
		}
	}
	return out
}

func (a *analysis) neowiseCatch() string {
	ws := a.parityWindows()
	for k := range ws {
		for p := range 2 {
			w := ws[k][p]
			if !a.surveyShows(w, lc.NEOWISE) || a.surveyShows(w, lc.ZTF) {
				continue
			}
			dep := w.dsum[lc.NEOWISE] / w.dnorm[lc.NEOWISE]
			return fmt.Sprintf("Caught %s on %s cycle, which ZTF never saw, on %s, %s mag deep.",
				a.dipName(k), parityArticle(p), fmtDate(a.minAt(w, lc.NEOWISE)), mag2(dep))
		}
	}
	return ""
}

func (a *analysis) tessRow(si surveyInfo, fits []SurveyFit) check.Check {
	mins := a.tessMinima()
	secs := map[int]bool{}
	unknown := 0
	for _, g := range si.groups {
		for _, se := range a.d.groups[g].series {
			if n := a.d.series[se].sector; n > 0 {
				secs[n] = true
			} else {
				unknown++
			}
		}
	}
	list := make([]int, 0, len(secs))
	for n := range secs {
		list = append(list, n)
	}
	slices.Sort(list)
	names := make([]string, len(list))
	for i, n := range list {
		names[i] = strconv.Itoa(n)
	}
	var head string
	switch {
	case len(list) == 1 && unknown == 0:
		head = "Sector " + names[0]
	case len(list) > 1 && unknown == 0:
		head = "Sectors " + joinList(names)
	default:
		head = fmt.Sprintf("TESS %s", si.span)
	}
	var timed []tessMin
	faint := map[int]bool{}
	for _, x := range mins {
		if x.faint {
			faint[x.sector] = true
		}
	}
	shared := 0
	for _, x := range mins {
		switch {
		case x.shared:
			delete(faint, x.sector)
			shared++
		case !x.faint:
			delete(faint, x.sector)
			timed = append(timed, x)
		}
	}
	note := faintNote(faint) + sharedNote(shared)
	if len(timed) == 0 {
		f := fits[si.groups[0]]
		if f.InDip1+f.InDip2 == 0 {
			return check.New(check.TESS, check.NotRun, head+": fell between dips.")
		}
		if !f.Agrees {
			return check.New(check.TESS, check.Fail, head+": the points in the dips are not fainter.")
		}
		if note != "" {
			return check.New(check.TESS, check.Pass, head+": as deep as predicted"+note+".")
		}
		return check.New(check.TESS, check.Pass, head+": caught only parts of dips, as deep as predicted.")
	}
	worst, worstBad, bad := 0.0, 0.0, 0
	for _, x := range timed {
		worst = math.Max(worst, math.Abs(x.lag))
		if !x.good {
			bad++
			worstBad = math.Max(worstBad, math.Abs(x.lag))
		}
	}
	if bad > 0 {
		return check.New(check.TESS, check.Fail, fmt.Sprintf("%s: of %s, %d miss the predicted time or depth, by up to %s%s.",
			head, plural(len(timed), "dip", "dips"), bad, minutes(worstBad), note))
	}
	if len(timed) == 1 {
		return check.New(check.TESS, check.Pass, fmt.Sprintf("%s: 1 dip, %s from the predicted time%s.", head, minutes(worst), note))
	}
	return check.New(check.TESS, check.Pass, fmt.Sprintf("%s: %s, each within %s of the predicted time%s.", head, plural(len(timed), "dip", "dips"), minutes(worst), note))
}

// sharedNote counts the dips set aside because other stars in the sector
// dimmed with the star at the time.
func sharedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %s set aside where nearby stars dim at the same time", plural(n, "dip", "dips"))
}

// faintNote names the sectors whose dips were too faint to time, blended
// with neighbours in TESS's large pixels.
func faintNote(faint map[int]bool) string {
	if len(faint) == 0 {
		return ""
	}
	list := make([]int, 0, len(faint))
	for n := range faint {
		list = append(list, n)
	}
	slices.Sort(list)
	names := make([]string, len(list))
	for i, n := range list {
		names[i] = strconv.Itoa(n)
	}
	if len(names) == 1 {
		return "; sector " + names[0] + " is too blended to time its dips"
	}
	return "; sectors " + joinList(names) + " are too blended to time their dips"
}

func (a *analysis) sineRow(s string, si surveyInfo, fits []SurveyFit) check.Check {
	id := surveyID(s)
	base := fmt.Sprintf("%s %s.", si.counts, si.span)
	best, bestN := -1, 0
	agrees := true
	for _, g := range si.groups {
		agrees = agrees && fits[g].Agrees
		if a.d.groups[g].points > bestN {
			best, bestN = g, a.d.groups[g].points
		}
	}
	amp := 2 * a.sine.amp[best]
	if !agrees {
		return check.New(id, check.NotRun, fmt.Sprintf("%s Too faint a change here to see the %s day cycle.", base, fmtPeriod(a.sine.P, a.sine.res.sigma(0))))
	}
	return check.New(id, check.Pass, fmt.Sprintf("%s Rises and falls by %s mag in %s every %s days.%s",
		base, mag2(amp), bandLabel(a.d.groups[best]), fmtPeriod(a.sine.P, a.sine.res.sigma(0)), blendNote(si)))
}

// reachNights is how many nights a flat survey light curve's points must
// reach the reported change on to count as seeing it: 17060 (Cygnus Loop
// Panel 2) had 1,274 ZTF r points steady at 0.011 mag scatter and its two
// points that reached the reported 0.22 mag fade both came from one night,
// MJD 60141, while the user's own frames showed no change on 11 nights.
const reachNights = 2

func (a *analysis) flatRow(s string, si surveyInfo) check.Check {
	id := surveyID(s)
	best, bestN := -1, 0
	for _, g := range si.groups {
		if a.d.groups[g].points > bestN {
			best, bestN = g, a.d.groups[g].points
		}
	}
	gr := a.d.groups[best]
	var ys []float64
	level, levelN := math.NaN(), 0
	relative := false
	faint := math.Inf(-1)
	for _, se := range gr.series {
		sr := a.d.series[se]
		relative = relative || sr.relative
		if len(sr.raw) > levelN {
			level, levelN = sr.level, len(sr.raw)
		}
		for _, p := range sr.raw {
			ys = append(ys, p.y)
			faint = math.Max(faint, p.y+a.d.series[sr.lead].level)
		}
	}
	scatter := mad(ys, median(ys))
	label := plural(gr.points, "point", "points")
	if gr.band != "" {
		label = commas(gr.points) + " " + gr.band + " points"
	}
	var txt string
	if a.kind == KindIrregular {
		lo, hi := percentile(ys, 2), percentile(ys, 98)
		txt = fmt.Sprintf("Varies: %s %s spread over %s mag with no repeating period.", label, si.span, mag2(hi-lo))
	} else {
		txt = fmt.Sprintf("Flat: %s %s", label, si.span)
		if relative {
			txt += fmt.Sprintf(", %s mag scatter.", mag2(scatter))
		} else {
			txt += fmt.Sprintf(", steady at %s with %s mag scatter. The faintest point is %s.", mag2(level), mag2(scatter), mag2(faint))
		}
	}
	c := a.opts.ChangeMag
	if a.kind == KindIrregular {
		return check.New(id, check.Pass, txt)
	}
	if c == 0 {
		return check.New(id, check.NotRun, txt)
	}
	word := "fade"
	if c < 0 {
		word = "brightening"
	}
	size := mag2(math.Abs(c))
	seen := 0
	nights := map[float64]bool{}
	for _, g := range si.groups {
		for _, se := range a.d.groups[g].series {
			for _, p := range a.d.series[se].raw {
				if math.Copysign(1, c)*p.y >= 0.6*math.Abs(c) {
					seen++
					nights[math.Floor(p.t)] = true
				}
			}
		}
	}
	strict := s == lc.ZTF || s == lc.ATLAS
	reached := fmt.Sprintf("%s reach the reported %s mag %s.", plural(seen, "point", "points"), size, word)
	if s == lc.ZTF && !a.steadyRun && math.Abs(c) >= 5*scatter {
		a.steadyRun = true
		if runs, backed := a.backedFades(c); backed > 0 {
			a.steadyAt = a.steadyNights(runs, c)
		}
	}
	if s == lc.ZTF && seen >= 2 && len(nights) >= reachNights && math.Abs(c) >= 5*scatter {
		if runs, backed := a.backedFades(c); backed >= fadeNights {
			a.fades = a.testFades(runs, backed, c)
			return a.fadeRow(id, label, si, level, scatter, reached)
		}
	}
	switch {
	case seen >= 2 && len(nights) >= reachNights:
		return check.New(id, check.Pass, txt+" "+reached)
	case math.Abs(c) < 5*scatter:
		return check.New(id, check.NotRun, fmt.Sprintf("%s Too noisy to show a %s mag %s.", txt, size, word))
	case strict:
		return check.New(id, check.Fail, fmt.Sprintf("%s A %s mag %s would have shown.", txt, size, word))
	}
	return check.New(id, check.NotRun, fmt.Sprintf("%s A %s mag %s would have shown here too.", txt, size, word))
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	i := int(p / 100 * float64(len(s)-1))
	return s[i]
}

func (a *analysis) foldSine() check.Check {
	s := a.sine
	g := a.refGroupSine()
	head := fmt.Sprintf("Smooth cycle of %s mag in %s", mag2(2*s.amp[g]), bandLabel(a.d.groups[g]))
	prof := func(p obs, tau float64) float64 {
		return s.amp[p.g] * math.Cos(2*math.Pi*(p.t-tau-s.T0)/s.P)
	}
	n, bad := a.seasonShifts(prof, func(obs) bool { return true }, 0.25*s.P)
	switch {
	case bad != "":
		return check.New(check.Fold, check.Fail, fmt.Sprintf("%s, but %s.", head, bad))
	case n == 0:
		return check.New(check.Fold, check.NotRun, head+", but no season has enough points to check the timing.")
	}
	return check.New(check.Fold, check.Pass, head+", lines up in every season.")
}

func (a *analysis) refGroupSine() int {
	best, bestN := 0, -1
	for g, gr := range a.d.groups {
		n := gr.points
		if gr.key == lc.ZTF+" r" {
			n += 1 << 30
		}
		if n > bestN {
			best, bestN = g, n
		}
	}
	return best
}

func (a *analysis) halfSine() check.Check {
	s := a.sine
	half := fmtPeriod(s.P/2, s.res.sigma(0)/2)
	h := fitSine(a.f, s.P/2, false)
	if h.robust-s.robust > modelGap {
		return check.New(check.Half, check.Pass, fmt.Sprintf("A cycle of %s days fits the points far worse, so the period is not half.", half))
	}
	return check.New(check.Half, check.Fail, fmt.Sprintf("A cycle of %s days fits as well, so the period may be %s days.", half, half))
}

func (a *analysis) doubleSine() check.Check {
	s := a.sine
	dbl := fmtPeriod(2*s.P, 2*s.res.sigma(0))
	type acc struct{ cc, ss, cs, yc, ys float64 }
	sums := make([][2]acc, len(a.d.groups))
	counts := make([][2]int, len(a.d.groups))
	a.rawPoints(func(_ *series, p obs) {
		x := (p.t - s.T0) / s.P
		par := ((int(math.Floor(x)) % 2) + 2) % 2
		th := 2 * math.Pi * x
		c, sn := math.Cos(th), math.Sin(th)
		w := 1 / (p.e * p.e)
		q := &sums[p.g][par]
		q.cc += w * c * c
		q.ss += w * sn * sn
		q.cs += w * c * sn
		q.yc += w * p.y * c
		q.ys += w * p.y * sn
		counts[p.g][par]++
	})
	compared := 0
	for g := range sums {
		if counts[g][0] < 10 || counts[g][1] < 10 {
			continue
		}
		var ab [2][2]float64
		var cov [2][3]float64
		ok := true
		for p := range 2 {
			q := sums[g][p]
			det := q.cc*q.ss - q.cs*q.cs
			if det <= 0 {
				ok = false
				break
			}
			ab[p] = [2]float64{(q.ss*q.yc - q.cs*q.ys) / det, (q.cc*q.ys - q.cs*q.yc) / det}
			cov[p] = [3]float64{q.ss / det, q.cc / det, -q.cs / det}
		}
		if !ok {
			continue
		}
		compared++
		da, db := ab[0][0]-ab[1][0], ab[0][1]-ab[1][1]
		va, vb, vab := cov[0][0]+cov[1][0], cov[0][1]+cov[1][1], cov[0][2]+cov[1][2]
		det := va*vb - vab*vab
		if det <= 0 {
			continue
		}
		chi := (vb*da*da - 2*vab*da*db + va*db*db) / det
		if chi > 25 && math.Hypot(da, db) > 0.2*s.amp[g] {
			return check.New(check.Double, check.Fail, fmt.Sprintf("Odd and even cycles differ in %s, so the true period may be %s days.", bandLabel(a.d.groups[g]), dbl))
		}
	}
	if compared == 0 {
		return check.New(check.Double, check.NotRun, fmt.Sprintf("Too few points on odd and even cycles to rule out %s days.", dbl))
	}
	return check.New(check.Double, check.Pass, fmt.Sprintf("Odd and even cycles match, so the period is not %s days.", dbl))
}
