package period

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func surveyOrder() []string { return []string{lc.ZTF, lc.ATLAS, lc.TESS, lc.NEOWISE} }

func (a *analysis) rawPoints(fn func(se *series, p obs)) {
	for _, se := range a.d.series {
		var off float64
		switch {
		case a.ecl != nil:
			off = a.ecl.offset[se.idx]
		case a.sine != nil:
			off = a.sine.offset[se.idx]
		default:
			off = a.flatOff[se.idx]
		}
		for _, p := range se.raw {
			fn(se, obs{t: p.t - a.f.tref, y: p.y - off, e: fitErr(p.e), g: se.group, s: se.idx})
		}
	}
}

func (a *analysis) dipName(k int) string {
	if len(a.ecl.dips) == 1 {
		return "the dip"
	}
	if k == 0 {
		return "the deep dip"
	}
	return "the shallow dip"
}

func parityArticle(p int) string {
	return "an " + parityName(p)
}

func parityName(p int) string {
	if p == 0 {
		return "even"
	}
	return "odd"
}

type tessMin struct {
	sector int
	lag    float64
	err    float64
	pred   float64
	good   bool
}

func (a *analysis) tessMinima() []tessMin {
	m := a.ecl
	if m == nil {
		return nil
	}
	var out []tessMin
	for _, se := range a.d.series {
		if se.survey != lc.TESS || len(se.raw) == 0 {
			continue
		}
		t0, t1 := se.raw[0].t-a.f.tref, se.raw[len(se.raw)-1].t-a.f.tref
		for k, dp := range m.dips {
			half := dp.width * m.P
			for c := math.Floor((t0-m.T0)/m.P - dp.phase - 1); ; c++ {
				tm := m.T0 + (c+dp.phase)*m.P
				if tm > t1+half {
					break
				}
				if tm < t0-half {
					continue
				}
				var pts []obs
				var left, right, core bool
				for _, p := range se.raw {
					dt := p.t - a.f.tref - tm
					if math.Abs(dt) >= 1.5*half {
						continue
					}
					u := dt / half
					left = left || (u < -0.3 && u > -1)
					right = right || (u > 0.3 && u < 1)
					core = core || math.Abs(u) <= 0.3
					pts = append(pts, obs{t: dt, y: p.y - m.offset[se.idx], e: math.Hypot(p.e, 0.002)})
				}
				if !left || !right || !core || len(pts) < 5 {
					continue
				}
				depth := m.depth[se.group][k]
				prof := func(p obs, tau float64) float64 {
					sh := shapeOnly((p.t-tau)/m.P, dp.width, dp.shape)
					return depth * sh
				}
				sf, ok := fitShift(pts, prof, 0.5*half, false)
				if !ok {
					continue
				}
				pred := math.Hypot(m.res.sigma(1), c*m.res.sigma(0)) * 1440
				lag := sf.tau * 1440
				good := math.Abs(lag) <= 5*math.Hypot(sf.tauErr*1440, pred)+5 && sf.scale > 0.5 && sf.scale < 1.5
				out = append(out, tessMin{sector: se.sector, lag: lag, err: sf.tauErr * 1440, pred: pred, good: good})
			}
		}
	}
	return out
}

func (a *analysis) surveyFits() []SurveyFit {
	var tm []tessMin
	if a.ecl != nil {
		tm = a.tessMinima()
	}
	out := make([]SurveyFit, 0, len(a.d.groups))
	for g, gr := range a.d.groups {
		sf := SurveyFit{Survey: gr.survey, Band: gr.band, Points: gr.points, Agrees: true}
		if a.ecl != nil {
			m := a.ecl
			var in [2]int
			var core [2]int
			a.rawPoints(func(se *series, p obs) {
				if se.group != g {
					return
				}
				for k := range m.dips {
					if sh, _ := m.shapeAt(p.t, k); sh > 0 {
						in[k]++
						if sh > 0.5 {
							core[k]++
						}
					}
				}
			})
			sf.InDip1, sf.InDip2 = in[0], in[1]
			for k := range m.dips {
				dep := 0.0
				if m.active[g][k] {
					dep = m.depth[g][k]
					if core[k] >= 3 && dep < 3*m.depthSigma(g, k) {
						sf.Agrees = false
					}
				}
				if k == 0 {
					sf.Depth1 = dep
				} else {
					sf.Depth2 = dep
				}
			}
			if gr.survey == lc.TESS {
				for _, x := range tm {
					sf.Minima++
					sf.MaxLagM = math.Max(sf.MaxLagM, math.Abs(x.lag))
					if !x.good {
						sf.Agrees = false
					}
				}
			}
		}
		if a.sine != nil {
			sf.Depth1 = 2 * a.sine.amp[g]
			sf.Agrees = a.sine.amp[g] > 3*a.sine.res.sigma(2+g)
		}
		out = append(out, sf)
	}
	return out
}

func (a *analysis) checks(fits []SurveyFit) []check.Check {
	var out []check.Check
	switch a.kind {
	case KindEclipsing:
		out = append(out, a.foldEclipse(), a.halfEclipse(), a.doubleEclipse())
	case KindSinusoidal:
		out = append(out, a.foldSine(), a.halfSine(), a.doubleSine())
	case KindFlat, KindIrregular:
		why := "No period to fold."
		out = append(out, a.periodRow(), check.New(check.Fold, check.NotRun, why),
			check.New(check.Half, check.NotRun, why), check.New(check.Double, check.NotRun, why))
	}
	for _, s := range surveyOrder() {
		out = append(out, a.surveyRow(s, fits))
	}
	return out
}

func (a *analysis) periodRow() check.Check {
	if a.kind == KindFlat {
		return check.New(check.Period, check.NotRun, "No period to search: the light curve is flat.")
	}
	lo, hi := a.spread()
	return check.New(check.Period, check.NotRun, fmt.Sprintf("No period stood out: the light curve varies by %s mag without repeating.", mag2(hi-lo)))
}

func (a *analysis) spread() (float64, float64) {
	var ys []float64
	a.rawPoints(func(_ *series, p obs) { ys = append(ys, p.y) })
	if len(ys) == 0 {
		return 0, 0
	}
	slices.Sort(ys)
	return ys[len(ys)*2/100], ys[len(ys)*98/100]
}

func (a *analysis) refGroup() int {
	best, bestN := 0, -1
	for g, gr := range a.d.groups {
		n := gr.points
		if gr.key == lc.ZTF+" r" {
			n += 1 << 30
		} else if gr.survey == lc.ZTF {
			n += 1 << 29
		}
		if a.ecl != nil && !a.ecl.active[g][0] {
			n -= 1 << 31
		}
		if n > bestN {
			best, bestN = g, n
		}
	}
	return best
}

func (a *analysis) seasons() [][]obs {
	var pts []obs
	onlyTiming := a.ecl != nil && a.ecl.timing
	a.rawPoints(func(se *series, p obs) {
		if se.survey != lc.TESS && (!onlyTiming || timingSurvey(se.survey)) {
			pts = append(pts, p)
		}
	})
	slices.SortStableFunc(pts, func(x, y obs) int {
		switch {
		case x.t < y.t:
			return -1
		case x.t > y.t:
			return 1
		}
		return 0
	})
	var out [][]obs
	cur := make([]obs, 0, len(pts))
	for i, p := range pts {
		if i > 0 && p.t-pts[i-1].t > 60 {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, p)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (a *analysis) seasonShifts(prof func(p obs, tau float64) float64, inside func(p obs) bool, maxShift float64) (int, string) {
	checked := 0
	for _, s := range a.seasons() {
		var pts []obs
		for _, p := range s {
			if inside(p) {
				pts = append(pts, p)
			}
		}
		if len(pts) < 8 {
			continue
		}
		sf, ok := fitShift(pts, prof, maxShift, false)
		if !ok || sf.scale < 3*sf.scaleErr || math.Abs(sf.tau) > 0.95*maxShift {
			continue
		}
		checked++
		limit := 5*sf.tauErr + 0.002*a.eph.Period
		if math.Abs(sf.tau) > limit {
			when := "late"
			if sf.tau < 0 {
				when = "early"
			}
			yr := bjdTime(pts[0].t + a.f.tref).Year()
			return checked, fmt.Sprintf("the %d season is %s hours %s", yr, strconv.FormatFloat(math.Abs(sf.tau)*24, 'f', 1, 64), when)
		}
	}
	return checked, ""
}

func (a *analysis) foldEclipse() check.Check {
	m := a.ecl
	g := a.refGroup()
	label := bandLabel(a.d.groups[g])
	var head string
	if len(m.dips) == 1 {
		head = fmt.Sprintf("One dip per cycle, %s mag deep in %s,", mag2(m.depth[g][0]), label)
	} else {
		head = fmt.Sprintf("Two dips per cycle, %s and %s mag deep in %s,", mag2(m.depth[g][0]), mag2(m.depth[g][1]), label)
	}
	prof := func(p obs, tau float64) float64 { return m.value(p.t-tau, p.g, -1) }
	inside := func(p obs) bool {
		for k := range m.dips {
			if sh, _ := m.shapeAt(p.t, k); sh > 0 {
				return true
			}
		}
		return false
	}
	n, bad := a.seasonShifts(prof, inside, 0.5*m.dips[0].width*m.P)
	verb := "line up"
	if len(m.dips) == 1 {
		verb = "lines up"
	}
	switch {
	case bad != "":
		return check.New(check.Fold, check.Fail, fmt.Sprintf("%s but %s.", strings.TrimSuffix(head, ","), bad))
	case n == 0:
		return check.New(check.Fold, check.NotRun, fmt.Sprintf("%s but no single season caught enough of a dip to check the timing.", strings.TrimSuffix(head, ",")))
	case n == 1:
		return check.New(check.Fold, check.Pass, fmt.Sprintf("%s %s in the only season with enough points in the dips.", head, verb))
	}
	return check.New(check.Fold, check.Pass, fmt.Sprintf("%s %s in every season.", head, verb))
}

func (a *analysis) halfEclipse() check.Check {
	m := a.ecl
	half := fmtPeriod(m.P/2, m.res.sigma(0)/2)
	if a.half.doubled {
		return check.New(check.Half, check.Pass, a.halfReason(a.half.reason, fmtPeriod(a.half.from, m.res.sigma(0)/2)))
	}
	if len(m.dips) == 2 {
		if why := a.dipsDiffer(m); why != "" {
			return check.New(check.Half, check.Pass, a.halfReason(why, half))
		}
		return check.New(check.Half, check.Fail, fmt.Sprintf("The two dips match in depth and sit half a cycle apart, so the period may be %s days.", half))
	}
	var smy, smm float64
	cycles := map[int]bool{}
	a.rawPoints(func(_ *series, p obs) {
		x := (p.t-m.T0)/m.P - 0.5
		u := wrapPhase(x)
		sh := shapeOnly(u, m.dips[0].width, m.dips[0].shape)
		if sh <= 0.5 {
			return
		}
		mm := m.depth[p.g][0] * sh
		w := 1 / (p.e * p.e)
		smy += w * p.y * mm
		smm += w * mm * mm
		cycles[int(math.Round(x))] = true
	})
	if smm == 0 || len(cycles) < 2 {
		return check.New(check.Half, check.NotRun, fmt.Sprintf("No survey caught the star half a cycle after a dip, so %s days is still possible.", half))
	}
	s, se := smy/smm, 1/math.Sqrt(smm)
	switch {
	case s < 0.3 && (1-s)/se > 5:
		return check.New(check.Half, check.Pass, fmt.Sprintf("Points half a cycle after each dip stay at the normal level, so the period is not %s days.", half))
	case s > 0.7:
		return check.New(check.Half, check.Fail, fmt.Sprintf("The star also dims half a cycle after each dip, so the period may be %s days.", half))
	}
	return check.New(check.Half, check.NotRun, fmt.Sprintf("Too few points half a cycle after a dip to rule out %s days.", half))
}

func (a *analysis) halfReason(why, half string) string {
	m := a.ecl
	switch why {
	case "depth":
		g, _ := a.depthDiff(m)
		if r := a.refGroup(); a.depthZ(m, r) >= 5 {
			g = r
		}
		if g >= 0 {
			return fmt.Sprintf("The two dips have different depths, %s and %s mag in %s, so the true period is not %s days.",
				mag2(m.depth[g][0]), mag2(m.depth[g][1]), bandLabel(a.d.groups[g]), half)
		}
		return fmt.Sprintf("The two dips have different depths, so the true period is not %s days.", half)
	case "phase":
		return fmt.Sprintf("The second dip comes at %s of the cycle, not halfway, so the true period is not %s days.",
			strconv.FormatFloat(m.dips[1].phase, 'f', 3, 64), half)
	}
	return fmt.Sprintf("The two dips last different times, so the true period is not %s days.", half)
}

type win struct {
	k        int
	smy, smm float64
	surveys  map[string]bool
	first    map[string]float64
	cycle    map[string]int
	sectors  map[int]bool
	dsum     map[string]float64
	dnorm    map[string]float64
	gy       map[int][2]float64
}

func (w *win) scale() (float64, float64) {
	if w.smm == 0 {
		return 0, math.Inf(1)
	}
	return w.smy / w.smm, 1 / math.Sqrt(w.smm)
}

func (w *win) shows() bool {
	s, e := w.scale()
	return s > 0.5 && s/e > 4
}

func (w *win) lacks() bool {
	s, e := w.scale()
	return s < 0.3 && (1-s)/e > 5
}

func (a *analysis) parityWindows() [][2]*win {
	m := a.ecl
	out := make([][2]*win, len(m.dips))
	for k := range out {
		for p := range 2 {
			out[k][p] = &win{k: k, surveys: map[string]bool{}, first: map[string]float64{}, cycle: map[string]int{}, sectors: map[int]bool{},
				dsum: map[string]float64{}, dnorm: map[string]float64{}, gy: map[int][2]float64{}}
		}
	}
	a.rawPoints(func(se *series, p obs) {
		for k := range m.dips {
			sh, _ := m.shapeAt(p.t, k)
			if sh < 0.5 {
				continue
			}
			cyc := int(math.Round((p.t-m.T0)/m.P - m.dips[k].phase))
			par := ((cyc % 2) + 2) % 2
			w := out[k][par]
			mm := m.depth[p.g][k] * sh
			wt := 1 / (p.e * p.e)
			w.smy += wt * p.y * mm
			w.smm += wt * mm * mm
			gy := w.gy[p.g]
			gy[0] += wt * p.y * mm
			gy[1] += wt * mm * mm
			w.gy[p.g] = gy
			w.surveys[se.survey] = true
			if f, ok := w.first[se.survey]; !ok || p.t < f {
				w.first[se.survey] = p.t
				w.cycle[se.survey] = cyc
			}
			if se.sector > 0 {
				w.sectors[se.sector] = true
			}
			w.dsum[se.survey] += wt * p.y * sh
			w.dnorm[se.survey] += wt * sh * sh
		}
	})
	return out
}

func (a *analysis) whoCaught(w *win, skip string) string {
	var parts []string
	for _, s := range surveyOrder() {
		if !w.surveys[s] || s == skip {
			continue
		}
		switch s {
		case lc.TESS:
			secs := make([]int, 0, len(w.sectors))
			for x := range w.sectors {
				secs = append(secs, x)
			}
			slices.Sort(secs)
			if len(secs) == 0 {
				parts = append(parts, "TESS")
				continue
			}
			ss := make([]string, len(secs))
			for i, x := range secs {
				ss[i] = strconv.Itoa(x)
			}
			word := "sector"
			if len(ss) > 1 {
				word = "sectors"
			}
			parts = append(parts, fmt.Sprintf("TESS %s %s", word, joinList(ss)))
		default:
			parts = append(parts, fmt.Sprintf("%s on %s", s, fmtDate(a.minAt(w, s))))
		}
	}
	return joinList(parts)
}

func (a *analysis) doubleEclipse() check.Check {
	m := a.ecl
	dbl := fmtPeriod(2*m.P, 2*m.res.sigma(0))
	ws := a.parityWindows()
	if c, ok := a.parityConflict(ws, dbl); ok {
		return c
	}
	shown := 0
	for k := range ws {
		for p := range 2 {
			if ws[k][p].shows() {
				shown++
			}
		}
	}
	if (len(m.dips) == 2 && shown >= 3) || (len(m.dips) == 1 && shown == 2) {
		return a.parityPass(ws, shown, dbl)
	}
	return a.parityUndecided(ws, dbl)
}

func (a *analysis) parityConflict(ws [][2]*win, dbl string) (check.Check, bool) {
	m := a.ecl
	for k := range ws {
		for p := range 2 {
			if ws[k][p].lacks() && ws[k][1-p].shows() {
				return check.New(check.Double, check.Fail, fmt.Sprintf("%s is missing on %s cycles, so the true period may be %s days.",
					capitalize(a.dipName(k)), parityName(p), dbl)), true
			}
		}
		for g := range a.d.groups {
			e, o := ws[k][0].gy[g], ws[k][1].gy[g]
			if e[1] == 0 || o[1] == 0 {
				continue
			}
			se, so := e[0]/e[1], o[0]/o[1]
			ee, eo := 1/math.Sqrt(e[1]), 1/math.Sqrt(o[1])
			if ee > 0.25 || eo > 0.25 {
				continue
			}
			if math.Abs(se-so) > 0.25 && math.Abs(se-so)/math.Hypot(ee, eo) > 4 {
				dd := m.depth[g][k]
				return check.New(check.Double, check.Fail, fmt.Sprintf("%s is %s mag deep on even cycles but %s on odd cycles in %s, so the true period may be %s days.",
					capitalize(a.dipName(k)), mag2(se*dd), mag2(so*dd), bandLabel(a.d.groups[g]), dbl)), true
			}
		}
	}
	return check.Check{}, false
}

func (a *analysis) parityPass(ws [][2]*win, shown int, dbl string) check.Check {
	if txt := a.extraParity(ws); txt != "" {
		return check.New(check.Double, check.Pass, "Ruled out: "+txt)
	}
	if len(a.ecl.dips) == 1 {
		return check.New(check.Double, check.Pass, fmt.Sprintf("The dip has the same depth on odd and even cycles, so the period is not %s days.", dbl))
	}
	if shown == 4 {
		return check.New(check.Double, check.Pass, "Ruled out: both dips show on odd and even cycles at the same depth.")
	}
	return check.New(check.Double, check.Pass, fmt.Sprintf("Ruled out: the surveys caught three different dips in %s days, more than one orbit can hold.", dbl))
}

func (a *analysis) parityUndecided(ws [][2]*win, dbl string) check.Check {
	var phrases []string
	who := map[string]bool{}
	for k := range ws {
		var ps []int
		for p := range 2 {
			if !ws[k][p].shows() {
				continue
			}
			ps = append(ps, p)
			for s := range ws[k][p].surveys {
				if a.surveyShows(ws[k][p], s) {
					who[s] = true
				}
			}
		}
		switch len(ps) {
		case 1:
			phrases = append(phrases, fmt.Sprintf("%s only on %s cycles", a.dipName(k), parityName(ps[0])))
		case 2:
			phrases = append(phrases, a.dipName(k)+" on both odd and even cycles")
		}
	}
	if len(phrases) == 0 {
		return check.New(check.Double, check.NotRun, fmt.Sprintf("Too few points in the dips to compare odd and even cycles, so %s days is still possible.", dbl))
	}
	return check.New(check.Double, check.NotRun, fmt.Sprintf("%s caught %s; no survey caught the other cycles yet, so %s days is still possible.",
		surveyNames(who), joinList(phrases), dbl))
}

func surveyNames(set map[string]bool) string {
	var out []string
	for _, s := range surveyOrder() {
		if set[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "The surveys"
	}
	return joinList(out)
}

func (a *analysis) surveyShows(w *win, s string) bool {
	n := w.dnorm[s]
	if n == 0 {
		return false
	}
	return w.dsum[s]/n*math.Sqrt(n) > 4
}

func (a *analysis) extraParity(ws [][2]*win) string {
	for k := range ws {
		for p := range 2 {
			w := ws[k][p]
			if !w.shows() || a.surveyShows(w, lc.ZTF) {
				continue
			}
			other := ws[k][1-p]
			var zt string
			if a.surveyShows(other, lc.ZTF) {
				zt = fmt.Sprintf("ZTF only saw %s on %s cycles", a.dipName(k), parityName(1-p))
			} else {
				zt = "ZTF never caught " + a.dipName(k)
			}
			var shownBy []string
			for _, s := range surveyOrder() {
				if s != lc.ZTF && a.surveyShows(w, s) {
					shownBy = append(shownBy, s)
				}
			}
			if len(shownBy) == 0 {
				continue
			}
			sub := &win{k: k, surveys: map[string]bool{}, cycle: w.cycle, sectors: w.sectors}
			for _, s := range shownBy {
				sub.surveys[s] = true
			}
			return fmt.Sprintf("%s, but %s caught it on %s cycles.", zt, a.whoCaught(sub, ""), parityName(p))
		}
	}
	return ""
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (a *analysis) minAt(w *win, s string) float64 {
	m := a.ecl
	return m.T0 + (float64(w.cycle[s])+m.dips[w.k].phase)*m.P + a.f.tref
}
