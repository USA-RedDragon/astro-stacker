package period

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Thresholds of the tests that catch periods ZTF's nightly sampling,
// slow changes or the sky itself put there; set on the 19 confirmed
// eclipsing binaries and the false periods of the 2026-10-17 audit.
const (
	shortPeriod   = 5.0
	hpMinSNR      = 5.0
	hpMaxFAP      = 1e-4
	rawMaxFAP     = 1e-6
	detrMaxFAP    = 1e-6
	cohMax        = 5.0
	compMaxFAP    = 1e-3
	compMaxSig    = 1
	compAmpRatio  = 4.0
	compAmpFloor  = 0.003
	compBoxRatio  = 2.5
	compBoxNone   = 6.0
	boxMinZ       = 12.0
	boxMinNights  = 2
	compClearBox  = 1.5
	compClearAmp  = 2.0
	seasonsPosMin = 0.6
	dipNights     = 3
	dipSeasons    = 2
	sidereal      = 1.0027379
	echoShare     = 0.0015
	echoNear      = 0.03
	lunar         = 29.530589
	year          = 365.25
	nightMinSpan  = 1.0 / 24
	nightMinN     = 5
	nightMinSlope = 0.5
	nightMinZ     = 3.0
	tessMinSNR    = 5.0
	tessMinZ      = 5.0
	tessMinShare  = 0.3
	groundShare   = 0.5
	groundRuleZ   = 3.0
)

// Robust is how a found period stands up to the tests that catch false
// periods in nightly sampled survey data.
type Robust struct {
	// Echo names the cycle the period sits next to, where nightly
	// sampling makes echoes: "one day", "half a day", "one sidereal
	// day", "the Moon's cycle", "a year"; empty when none.
	Echo string `json:"echo,omitempty"`
	// Stable says the period survives taking out the star's slow changes:
	// for a short period the cycle still stands out once changes slower
	// than about three weeks are removed; for a long one once each
	// season's level is removed, at the same phase every season. For
	// eclipses the dips stay deep after the slow changes are removed,
	// over several nights and seasons.
	Stable bool `json:"stable"`
	// Confirmed names a record that shows the period where ZTF's daily
	// sampling cannot: "TESS" or "changes within a night".
	Confirmed string `json:"confirmed,omitempty"`
	// TESS says TESS, which watches for weeks without daily gaps, shows
	// the period: at least two dips on time, or the same smooth cycle.
	TESS bool `json:"tess,omitempty"`
	// TESSOnly says TESS shows the deeper dip but ZTF and ATLAS, whose
	// pixels split the stars TESS's 21 arcsec pixels blend, measure it at
	// under groundShare of TESS's depth and rule out that share at
	// groundRuleZ sigma: the dips are another star's or TESS's noise.
	TESSOnly bool `json:"tess_only,omitempty"`
	// TESSDepth and GroundDepth are the deeper dip's depths behind
	// TESSOnly, in magnitudes with blending taken out.
	TESSDepth   float64 `json:"tess_depth,omitempty"`
	GroundDepth float64 `json:"ground_depth,omitempty"`
	// Comparisons is how many nearby stars of the same brightness were
	// tested; CompSignal how many show the period; CompRatio the star's
	// signal over their median.
	Comparisons int     `json:"comparisons"`
	CompSignal  int     `json:"comp_signal"`
	CompRatio   float64 `json:"comp_ratio"`
	// CompPass says nearby steady stars do not show the period.
	// CompClear says they plainly do: two or more show it, or the star's
	// signal is under compClearAmp (smooth cycles) or compClearBox (dips)
	// times theirs; between that and a pass the comparison cannot tell.
	// For dips CompSignal counts the comparison stars with a box at
	// boxMinZ.
	CompPass  bool `json:"comp_pass"`
	CompClear bool `json:"comp_clear,omitempty"`
	// Real says one band passes the stable and comparison tests together.
	Real bool `json:"real"`
	// Partner is the period that stands out once the slow changes are
	// removed, or the one TESS shows, when it differs from the found one.
	Partner     float64 `json:"partner,omitempty"`
	PartnerFrom string  `json:"partner_from,omitempty"`
	// Band is the survey band the result rests on.
	Band string `json:"band,omitempty"`
	// Bands counts the survey bands in which a smooth cycle survives
	// taking out the slow changes, at a false alarm chance under
	// rawMaxFAP once they are out, and stands above the comparison stars.
	Bands int `json:"bands,omitempty"`
	// Alias is the period the search found first, and AliasEcho the cycle
	// it sits next to, when the period is that period's partner: the
	// cycle the bands show together once slow changes are taken out,
	// promoted because the first one was an echo nothing confirms.
	Alias     float64 `json:"alias,omitempty"`
	AliasEcho string  `json:"alias_echo,omitempty"`
	// TESSPeriod is the smooth cycle TESS shows on its own, when it shows
	// one.
	TESSPeriod float64 `json:"tess_period,omitempty"`
	// HPSNR is the cycle's signal to noise ratio once slow changes are
	// removed, in Band; BoxZ the same for an eclipse's depth.
	HPSNR float64 `json:"hp_snr,omitempty"`
	BoxZ  float64 `json:"box_z,omitempty"`
	// Nights and Seasons count the survey nights and seasons with points
	// inside an eclipse.
	Nights  int `json:"nights,omitempty"`
	Seasons int `json:"seasons,omitempty"`
	// Unsteady is set for a long smooth cycle whose timing differs from
	// season to season; Few for eclipses caught on too few nights or
	// seasons.
	Unsteady bool `json:"unsteady,omitempty"`
	Few      bool `json:"few,omitempty"`
}

// echoOf names the cycle a period sits next to, for data spanning span
// days, or "": a whole number of days or a day over a whole number, solar
// or sidereal, and for a smooth cycle also the Moon's month or the year
// or half of either, whose sky brightness and seasons make slow smooth
// changes but not narrow dips.
func echoOf(p, span float64, smooth bool) string {
	if !(p > 0) {
		return ""
	}
	f := 1 / p
	tol := func(k float64) float64 {
		t := echoShare * k
		if span > 0 {
			t = math.Max(t, 2/span)
		}
		return t
	}
	names := map[float64]string{1: "one day", 2: "half a day", 3: "a third of a day", 4: "a quarter of a day",
		1.0 / 2: "two days", 1.0 / 3: "three days", 1.0 / 4: "four days"}
	for _, k := range []float64{1, 2, 3, 4} {
		for _, kk := range []float64{k, 1 / k} {
			solar := math.Abs(f-kk) < tol(kk)
			if !solar && math.Abs(f-kk*sidereal) >= tol(kk*sidereal) {
				continue
			}
			if kk == 1 && !solar {
				return "one sidereal day"
			}
			return names[kk]
		}
	}
	if !smooth {
		return ""
	}
	for _, n := range []float64{1, 2} {
		if math.Abs(p/(lunar/n)-1) < echoNear {
			return "the Moon's cycle"
		}
		if math.Abs(p/(year/n)-1) < echoNear {
			return "a year"
		}
	}
	return ""
}

// band is one survey band's points for the tests.
type band struct {
	g      int
	key    string
	raw    []pt
	hp     []pt
	detr   []pt
	weight []float64
}

// groupPts are group g's points, each series on its own median, the light
// curves an input series merges aligned on its largest one's zero point,
// and each input series passed through fn when set, in time order; TESS's
// in its half hour bins.
func groupPts(d *data, g int, fn func([]pt) []pt) []pt {
	byLead := map[int][]pt{}
	var leads []int
	for _, si := range d.groups[g].series {
		se := d.series[si]
		src := se.raw
		if se.survey == lc.TESS {
			src = se.srch
		}
		if _, ok := byLead[se.lead]; !ok {
			leads = append(leads, se.lead)
		}
		for _, o := range src {
			byLead[se.lead] = append(byLead[se.lead], pt{t: o.t, y: o.y - se.shift, e: math.Max(o.e, minErr)})
		}
	}
	var out []pt
	for _, l := range leads {
		ps := byLead[l]
		if fn != nil {
			sortPts(ps)
			ps = fn(ps)
		}
		out = append(out, ps...)
	}
	sortPts(out)
	return out
}

func testedSurvey(s string) bool { return s == lc.ZTF || s == lc.ATLAS }

func (a *analysis) bands() []*band {
	if a.bandsDone {
		return a.bandCache
	}
	a.bandsDone = true
	for g, gr := range a.d.groups {
		if !testedSurvey(gr.survey) && gr.survey != lc.TESS {
			continue
		}
		raw := groupPts(a.d, g, nil)
		if len(raw) < minBand {
			continue
		}
		b := &band{g: g, key: gr.key, raw: raw, hp: groupPts(a.d, g, highPass), detr: groupPts(a.d, g, detrendSeasons)}
		if testedSurvey(gr.survey) {
			b.weight = nightWeights(b.hp)
		}
		a.bandCache = append(a.bandCache, b)
	}
	return a.bandCache
}

// compBands are the comparison stars' points per band key.
func compBands(comps [][]lc.Series) []map[string][]pt {
	out := make([]map[string][]pt, 0, len(comps))
	for _, c := range comps {
		d := prepare(c)
		m := map[string][]pt{}
		for g, gr := range d.groups {
			if p := groupPts(d, g, nil); len(p) >= minBand {
				m[gr.key] = p
			}
		}
		if len(m) > 0 {
			out = append(out, m)
		}
	}
	return out
}

func (a *analysis) robust(comps [][]lc.Series) *Robust {
	e := a.eph
	if e == nil || e.Period <= 0 {
		return nil
	}
	r := &Robust{Echo: echoOf(e.Period, a.ztfSpan(), a.kind == KindSinusoidal)}
	cb := compBands(comps)
	r.Comparisons = len(cb)
	switch a.kind {
	case KindSinusoidal:
		a.robustSine(r, cb)
	case KindEclipsing:
		a.robustEclipse(r, cb)
	case KindFlat, KindIrregular:
		return nil
	}
	if a.kind == KindEclipsing {
		r.TESSOnly, r.TESSDepth, r.GroundDepth = a.tessOnly()
	}
	r.TESS = !r.TESSOnly && a.tessConfirms()
	if r.Echo != "" {
		r.Confirmed = a.confirm(r.TESS)
	}
	if a.kind == KindSinusoidal {
		if p, ok := a.tessPeriod(); ok {
			r.TESSPeriod = p
			if r.Partner == 0 && math.Abs(p/e.Period-1) > 0.01 {
				r.Partner, r.PartnerFrom = p, lc.TESS
			}
		}
	}
	return r
}

func (a *analysis) ztfSpan() float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, se := range a.d.series {
		if se.survey != lc.ZTF || len(se.raw) == 0 {
			continue
		}
		lo, hi = math.Min(lo, se.raw[0].t), math.Max(hi, se.raw[len(se.raw)-1].t)
	}
	if hi <= lo {
		return a.d.t1 - a.d.t0
	}
	return hi - lo
}

type sineBand struct {
	b          *band
	sig        bool
	stable     bool
	snr        float64
	unsteady   bool
	compSig    int
	compN      int
	compRatio  float64
	compOK     bool
	partner    float64
	partnerFAP float64
	slowFAP    float64
}

func (s *sineBand) rank() int {
	n := 0
	if s.stable && s.compOK && s.sig {
		n += 4
	}
	if s.stable {
		n += 2
	}
	if s.compOK {
		n++
	}
	return n
}

// slowTest is A1 for a smooth cycle in one band: under shortPeriod days
// the cycle must stand out once changes slower than three weeks are
// taken out; longer, once each season's level is taken out, at the same
// phase every season. Either way the period must be among the three
// highest peaks left.
func (s *sineBand) slowTest(per float64) {
	b := s.b
	pts := b.hp
	if per >= shortPeriod {
		pts = b.detr
	}
	f, ok := fitSineAt(pts, per)
	if ok && f.ampErr > 0 {
		s.snr = f.amp / f.ampErr
	}
	var top []float64
	if pg := newPeriodogram(pts); pg != nil {
		top = pg.top(3)
	}
	p := fap(powerNear(pts, per), pts)
	s.slowFAP = p
	if per < shortPeriod {
		s.stable = ok && s.snr >= hpMinSNR && p < hpMaxFAP && amongPeriods(per, top)
	} else {
		red, cok := coherence(b.raw, per)
		s.unsteady = cok && red >= cohMax
		s.stable = cok && red < cohMax && p < detrMaxFAP && amongPeriods(per, top)
	}
	if len(top) > 0 && !amongPeriods(per, top[:1]) {
		s.partner, s.partnerFAP = top[0], fap(powerNear(pts, top[0]), pts)
	}
}

// compTest is A2 for a smooth cycle in one band: at most compMaxSig of
// the comparison stars may show the period, and the star's amplitude must
// be compAmpRatio times their median (compAmpFloor without any). When
// comparison stars exist but none in this band, as for ATLAS, the band
// cannot pass.
func (s *sineBand) compTest(per float64, cb []map[string][]pt) {
	amp := 0.0
	if f, ok := fitSineAt(s.b.raw, per); ok {
		amp = f.amp
	}
	var camps []float64
	for _, c := range cb {
		cp, ok := c[s.b.key]
		if !ok {
			continue
		}
		s.compN++
		if fap(powerNear(cp, per), cp) < compMaxFAP {
			s.compSig++
		}
		if f, ok := fitSineAt(cp, per); ok {
			camps = append(camps, f.amp)
		}
	}
	cm := compAmpFloor
	if len(camps) > 0 {
		cm = math.Max(median(camps), 1e-4)
	}
	s.compRatio = amp / cm
	s.compOK = s.compSig <= compMaxSig && amp >= compAmpRatio*cm && (s.compN > 0 || len(cb) == 0)
}

func (a *analysis) robustSine(r *Robust, cb []map[string][]pt) {
	P := a.eph.Period
	var best *sineBand
	var all []*sineBand
	for _, b := range a.bands() {
		if !testedSurvey(a.d.groups[b.g].survey) {
			continue
		}
		s := &sineBand{b: b, sig: fap(powerNear(b.raw, P), b.raw) < rawMaxFAP}
		s.slowTest(P)
		s.compTest(P, cb)
		all = append(all, s)
		if s.stable && s.compOK && s.slowFAP < rawMaxFAP {
			r.Bands++
		}
		if best == nil || s.rank() > best.rank() {
			best = s
		}
	}
	if best == nil {
		return
	}
	if !best.stable {
		best.partner, best.partnerFAP = sharedPartner(all, best)
	}
	r.Band = best.b.key
	r.Stable, r.Unsteady = best.stable, best.unsteady
	r.HPSNR = best.snr
	r.CompSignal, r.CompRatio, r.CompPass = best.compSig, best.compRatio, best.compOK
	r.CompClear = !best.compOK && (best.compSig > compMaxSig || best.compRatio < compClearAmp)
	r.Comparisons = best.compN
	r.Real = best.stable && best.compOK && best.sig
	if !best.stable && best.partner > 0 && best.partnerFAP < rawMaxFAP {
		r.Partner, r.PartnerFrom = best.partner, best.b.key
	}
}

// sharedPartner is the one of the bands' partner periods that all bands
// show best together, by each band's power at it times its points, with
// its false alarm chance in the band that found it. A single band's
// highest peak can be a daily echo of the true cycle: 167's ZTF g peaks
// at 0.810 days where g and r together show 4.318 days.
func sharedPartner(all []*sineBand, best *sineBand) (float64, float64) {
	per, pf, top := best.partner, best.partnerFAP, -1.0
	for _, c := range all {
		if c.partner <= 0 || c.partnerFAP >= rawMaxFAP {
			continue
		}
		score := 0.0
		for _, s := range all {
			pts := s.b.hp
			if c.partner >= shortPeriod {
				pts = s.b.detr
			}
			score += float64(len(pts)) * powerNear(pts, c.partner)
		}
		if score > top {
			per, pf, top = c.partner, c.partnerFAP, score
		}
	}
	return per, pf
}

type boxBand struct {
	b          *band
	box        box
	cmed       float64
	seasons    int
	pos        int
	nights     int
	stable     bool
	compOK     bool
	compN      int
	compStrong int
	compRatio  float64
}

func (a *analysis) robustEclipse(r *Robust, cb []map[string][]pt) {
	P := a.eph.Period
	nights, seasons := a.dipEpochs(a.ecl)
	r.Nights, r.Seasons = nights, seasons
	epochs := nights >= dipNights && seasons >= dipSeasons
	var good []*boxBand
	var best *boxBand
	for _, b := range a.bands() {
		if !testedSurvey(a.d.groups[b.g].survey) {
			continue
		}
		s := &boxBand{b: b, box: boxSearch(b.hp, P, b.weight, a.dipWhere(a.ecl))}
		s.seasons, s.pos = seasonDepths(b.hp, P, s.box)
		s.nights = boxNights(b.hp, P, s.box)
		var snrs []float64
		for _, c := range cb {
			if cp, ok := c[b.key]; ok {
				cs := boxSearch(cp, P, nightWeights(cp), nil).snr
				snrs = append(snrs, cs)
				if cs >= boxMinZ {
					s.compStrong++
				}
			}
		}
		s.compN = len(snrs)
		s.cmed = compBoxNone
		if len(snrs) > 0 {
			s.cmed = math.Max(median(snrs), 1e-3)
		}
		s.compRatio = s.box.snr / s.cmed
		seasonsOK := s.seasons == 0 || float64(s.pos) >= seasonsPosMin*float64(s.seasons)
		s.stable = s.box.snr >= boxMinZ && s.nights >= boxMinNights && seasonsOK && epochs
		s.compOK = s.box.snr >= compBoxRatio*s.cmed && (s.compN > 0 || len(cb) == 0)
		if s.stable && s.compOK {
			good = append(good, s)
		}
		if best == nil || boxRank(s) > boxRank(best) || (boxRank(s) == boxRank(best) && s.box.snr > best.box.snr) {
			best = s
		}
	}
	if best == nil {
		r.Few = !epochs
		return
	}
	r.Band = best.b.key
	r.Stable, r.CompPass, r.Few = best.stable, best.compOK, !epochs
	r.BoxZ, r.CompRatio, r.Comparisons = best.box.snr, best.compRatio, best.compN
	r.CompSignal = best.compStrong
	r.CompClear = !best.compOK && (best.compRatio < compClearBox || best.compStrong > compMaxSig)
	r.Real = len(good) > 0 && colourOK(good)
}

// dipWhere limits a box search to boxes centred within half a dip's
// length of one of the model's dips.
func (a *analysis) dipWhere(m *ecFit) *boxAt {
	if m == nil {
		return nil
	}
	w := &boxAt{}
	for _, dp := range m.dips {
		w.at = append(w.at, frac((m.T0+a.f.tref)/m.P+dp.phase))
		w.tol = math.Max(w.tol, dp.width)
	}
	return w
}

func boxRank(s *boxBand) int {
	n := 0
	if s.stable && s.compOK {
		n += 4
	}
	if s.stable {
		n += 2
	}
	if s.compOK {
		n++
	}
	return n
}

// colourOK says the depths of a dip in ZTF g and r, when both pass, are
// within a factor 3 of each other, as an eclipse's are.
func colourOK(good []*boxBand) bool {
	dep := map[string]float64{}
	for _, s := range good {
		dep[s.b.key] = s.box.depthMag
	}
	g, okg := dep[lc.ZTF+" g"]
	r, okr := dep[lc.ZTF+" r"]
	if !okg || !okr {
		return true
	}
	ratio := g / math.Max(r, 1e-3)
	return ratio > 1.0/3 && ratio < 3
}

// dipEpochs counts the distinct nights and seasons with points from any
// survey inside the deeper dip, fainter by at least half the model there.
func (a *analysis) dipEpochs(m *ecFit) (int, int) {
	if m == nil {
		return 0, 0
	}
	nights := map[int64]bool{}
	var times []float64
	for i := range a.f.t {
		if !m.keep[i] {
			continue
		}
		sh, _ := m.shapeAt(a.f.t[i], 0)
		dd := m.depth[a.f.g[i]][0]
		if sh > 0.5 && dd > 0 && a.f.y[i]-m.offset[a.f.s[i]] > 0.5*dd*sh {
			t := a.f.t[i] + a.f.tref
			if n := nightOf(t); !nights[n] {
				nights[n] = true
				times = append(times, t)
			}
		}
	}
	slices.Sort(times)
	seasons := 0
	for i, t := range times {
		if i == 0 || t-times[i-1] > seasonGap {
			seasons++
		}
	}
	return len(nights), seasons
}

// confirm names an independent record that shows the period where daily
// sampling cannot tell: TESS, or the size of changes within single nights.
func (a *analysis) confirm(tess bool) string {
	if tess {
		return lc.TESS
	}
	if a.nightConfirms() {
		return "changes within a night"
	}
	return ""
}

func (a *analysis) tessConfirms() bool {
	switch a.kind {
	case KindEclipsing:
		if !a.tessDeep() {
			return false
		}
		mins := a.tessMinima()
		good := 0
		for _, m := range mins {
			if m.faint {
				continue
			}
			if !m.good {
				return false
			}
			good++
		}
		return good >= 2
	case KindSinusoidal:
		p, ok := a.tessPeriod()
		return ok && math.Abs(p/a.eph.Period-1) < 0.01
	case KindFlat, KindIrregular:
	}
	return false
}

// tessDeep says TESS's own fit sees the deeper dip at tessMinZ sigma and
// at least tessMinShare of its depth in ZTF, both with blending taken
// out: on-time minima of a dip TESS barely sees confirm nothing.
func (a *analysis) tessDeep() bool {
	m := a.ecl
	ztf, tess := 0.0, 0.0
	for g, gr := range a.d.groups {
		if !m.active[g][0] {
			continue
		}
		d := undilute(m.depth[g][0], gr.dil)
		switch gr.survey {
		case lc.ZTF:
			ztf = math.Max(ztf, d)
		case lc.TESS:
			if m.depth[g][0] >= tessMinZ*m.depthSigma(g, 0) {
				tess = math.Max(tess, d)
			}
		}
	}
	return tess > 0 && tess >= tessMinShare*ztf
}

// tessOnly says the deeper dip TESS sees at tessMinZ sigma is under
// groundShare of its depth in every ZTF and ATLAS band that has points in
// it, and one of those bands rules that share out at groundRuleZ sigma,
// both with blending taken out. 17189 (Cygnus Loop Panel 2) showed a 0.09
// mag TESS dip every 26.83 days while ZTF and ATLAS, on 24 nights inside
// the dips, two of them during TESS's dips, measured 0.01 mag or less.
func (a *analysis) tessOnly() (bool, float64, float64) {
	m := a.ecl
	if m == nil {
		return false, 0, 0
	}
	tess, ground := 0.0, 0.0
	type gd struct{ d, s float64 }
	var gs []gd
	for g, gr := range a.d.groups {
		if !m.active[g][0] {
			continue
		}
		d := undilute(m.depth[g][0], gr.dil)
		switch gr.survey {
		case lc.TESS:
			if m.depth[g][0] >= tessMinZ*m.depthSigma(g, 0) {
				tess = math.Max(tess, d)
			}
		case lc.ZTF, lc.ATLAS:
			ground = math.Max(ground, d)
			gs = append(gs, gd{d, m.depthSigma(g, 0) / math.Min(math.Max(gr.dil, 1e-3), 1)})
		}
	}
	if tess == 0 || len(gs) == 0 || ground >= groundShare*tess {
		return false, 0, 0
	}
	for _, x := range gs {
		if x.d+groundRuleZ*x.s < groundShare*tess {
			return true, tess, ground
		}
	}
	return false, 0, 0
}

// tessPeriod is the strongest period in the TESS points, when its cycle
// stands out at tessMinSNR.
func (a *analysis) tessPeriod() (float64, bool) {
	var p []pt
	longest := 0.0
	for _, se := range a.d.series {
		if se.survey != lc.TESS || len(se.srch) < 20 {
			continue
		}
		for _, o := range se.srch {
			p = append(p, pt{t: o.t, y: o.y, e: math.Max(o.e, minErr)})
		}
		longest = math.Max(longest, se.srch[len(se.srch)-1].t-se.srch[0].t)
	}
	if len(p) < 50 || longest < 2 {
		return 0, false
	}
	sortPts(p)
	span := spanOf(p)
	maxP := math.Min(longest/2, a.opts.MaxPeriod)
	df := lsStep / span
	f0 := 1 / maxP
	n := int((1/math.Max(a.opts.MinPeriod, 0.2) - f0) / df)
	if n < 10 {
		return 0, false
	}
	pow := lsPower(toLS(p), f0, df, n)
	bi := 0
	for i := range pow {
		if pow[i] > pow[bi] {
			bi = i
		}
	}
	per := 1 / (f0 + float64(bi)*df)
	f, ok := fitSineAt(p, per)
	if !ok || f.ampErr <= 0 || f.amp/f.ampErr < tessMinSNR {
		return 0, false
	}
	return per, true
}

// siderealEcho says a period sits next to a sidereal day or a whole
// multiple or fraction of it, where the change it predicts within a night
// follows the hour angle, as airmass and extinction trends in the user's
// own frames do.
func siderealEcho(p float64) bool {
	f := 1 / p
	for _, k := range []float64{1, 2, 3, 4} {
		for _, kk := range []float64{k, 1 / k} {
			if math.Abs(f-kk*sidereal) < 0.01*kk*sidereal {
				return true
			}
		}
	}
	return false
}

// nightConfirms tests the period by the changes it predicts within single
// nights: on nights with two points at least an hour apart, the measured
// change must follow the predicted one.
func (a *analysis) nightConfirms() bool {
	var num, den float64
	nights := 0
	use := func(bandKey string, raw []obs) {
		byNight := map[int64][2]obs{}
		for _, o := range raw {
			n := nightOf(o.t)
			fl, ok := byNight[n]
			if !ok {
				fl = [2]obs{o, o}
			}
			if o.t < fl[0].t {
				fl[0] = o
			}
			if o.t > fl[1].t {
				fl[1] = o
			}
			byNight[n] = fl
		}
		for _, fl := range byNight {
			first, last := fl[0], fl[1]
			if last.t-first.t < nightMinSpan {
				continue
			}
			dp := a.eph.Predict(last.t, bandKey) - a.eph.Predict(first.t, bandKey)
			e := math.Hypot(math.Max(first.e, minErr), math.Max(last.e, minErr))
			w := 1 / (e * e)
			num += w * (last.y - first.y) * dp
			den += w * dp * dp
			nights++
		}
	}
	for _, se := range a.d.series {
		if se.survey == lc.ZTF {
			use(se.key, se.raw)
		}
	}
	for _, s := range a.own {
		if siderealEcho(a.eph.Period) {
			break
		}
		raw := make([]obs, 0, len(s.Points))
		for _, p := range s.Points {
			raw = append(raw, obs{t: p.BJD, y: p.Mag, e: p.Err})
		}
		use("", raw)
	}
	if nights < nightMinN || den <= 0 {
		return false
	}
	slope, se := num/den, 1/math.Sqrt(den)
	return slope >= nightMinSlope && slope/se >= nightMinZ
}
