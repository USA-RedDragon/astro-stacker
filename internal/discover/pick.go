package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	ClassEmission   = "emission"
	ClassRemnant    = "snr"
	ClassPlanetary  = "pn"
	ClassGalaxy     = "galaxy"
	ClassCluster    = "cluster"
	ClassReflection = "reflection"
	ClassDark       = "dark"

	FamilyNarrowband = "narrowband"
	FamilyBroadband  = "broadband"
	FamilyDust       = "dust"

	RuleSII = "sii"
	RuleHa  = "ha"

	PaletteHOO  = "HOO"
	PaletteLRGB = "LRGB"

	MinRuleSide     = 2
	MinHAlphaPoints = 3
	ScaleTolerance  = 0.05

	gaiaRelation = "Gaia EDR3 documentation §5.5.1 Table 5.7: G−V = −0.04749 − 0.0124(B−V) − 0.2901(B−V)² + 0.02008(B−V)³, σ 0.048, for −0.4 < B−V < 1.3"
	gaiaBVMin    = -0.4
	gaiaBVMax    = 1.3
	sbSourceNGC  = "openngc"
)

type PickRulePoint struct {
	Name     string  `json:"name"`
	Object   string  `json:"object"`
	Rayleigh float64 `json:"rayleigh"`
	Shot     bool    `json:"shot"`
}

type PickRule struct {
	Key       string          `json:"key"`
	Filter    string          `json:"filter"`
	Label     string          `json:"label"`
	Threshold *float64        `json:"threshold"`
	Derived   bool            `json:"derived"`
	Basis     string          `json:"basis"`
	Measured  *float64        `json:"measured"`
	Strict    bool            `json:"strict"`
	Met       bool            `json:"met"`
	Points    []PickRulePoint `json:"points"`
}

type PickHours struct {
	Hours   *float64 `json:"hours"`
	Low     *float64 `json:"low"`
	High    *float64 `json:"high"`
	Points  int      `json:"points"`
	Basis   string   `json:"basis"`
	Unknown string   `json:"unknown,omitempty"`
}

type PickTemplate struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Filter string `json:"filter"`
}

type PickFilter struct {
	Filter        string        `json:"filter"`
	Template      *PickTemplate `json:"template"`
	TemplateBasis string        `json:"templateBasis"`
	Exposure      *float64      `json:"exposure"`
	ExposureBasis string        `json:"exposureBasis"`
	Subs          int           `json:"subs"`
	Hours         PickHours     `json:"hours"`
}

type PickSet struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Projects int    `json:"projects"`
}

type ExposurePick struct {
	Object     catalog.Object `json:"object"`
	Class      string         `json:"class"`
	ClassLabel string         `json:"classLabel"`
	Palette    string         `json:"palette"`
	Reason     string         `json:"reason"`
	NoPick     string         `json:"noPick,omitempty"`
	HAlpha     *halpha.Sample `json:"halpha"`
	HAlphaMap  halpha.Status  `json:"halphaMap"`
	Rules      []PickRule     `json:"rules"`
	Filters    []PickFilter   `json:"filters"`
	Set        *PickSet       `json:"set"`
	Missing    []string       `json:"missing"`
	GoalSNR    float64        `json:"goalSnr"`
	skybright.Value
}

func PickClass(t string) (class, label string) {
	switch t {
	case catalog.TypeEmission, catalog.TypeNebula:
		return ClassEmission, "emission nebula"
	case catalog.TypeClusterNebula:
		return ClassEmission, "cluster with nebula"
	case catalog.TypeSNR:
		return ClassRemnant, "supernova remnant"
	case catalog.TypePN:
		return ClassPlanetary, "planetary nebula"
	case catalog.TypeGalaxy:
		return ClassGalaxy, "galaxy"
	case catalog.TypeGalaxyGroup:
		return ClassGalaxy, "galaxy group"
	case catalog.TypeOpenCluster:
		return ClassCluster, "open cluster"
	case catalog.TypeGlobular:
		return ClassCluster, "globular cluster"
	case catalog.TypeReflection:
		return ClassReflection, "reflection nebula"
	case catalog.TypeDark:
		return ClassDark, "dark nebula"
	}
	return "", t
}

func Family(class string) string {
	switch class {
	case ClassEmission, ClassRemnant, ClassPlanetary:
		return FamilyNarrowband
	case ClassGalaxy, ClassCluster:
		return FamilyBroadband
	case ClassReflection, ClassDark:
		return FamilyDust
	}
	return ""
}

func filterLabel(canon string) string {
	switch canon {
	case "H":
		return "H-α"
	case "O":
		return "O-III"
	case "S":
		return "S-II"
	}
	return canon
}

func rText(r float64) string {
	if r >= 10 {
		return fmt.Sprintf("%.0f R", r)
	}
	return fmt.Sprintf("%.1f R", r)
}

func hText(h float64) string {
	if h >= 10 {
		return fmt.Sprintf("%.0f h", h)
	}
	return fmt.Sprintf("%.1f h", h)
}

type pastTarget struct {
	subject Subject
	object  catalog.Object
	class   string
	sample  *halpha.Sample
	canon   map[string]float64
}

func (s *Service) pastTargets(snap *snapshot, hmap *halpha.Map) []pastTarget {
	var out []pastTarget
	for _, subj := range snap.subjects {
		if subj.Kind == SubjectAdding || subj.TotalHours() <= 0 {
			continue
		}
		var link *Link
		for _, l := range snap.bySubj[subj.Key] {
			if l.Linked() {
				link = &l
				break
			}
		}
		if link == nil {
			continue
		}
		class, _ := PickClass(link.Object.Type)
		if class == "" {
			continue
		}
		pt := pastTarget{subject: subj, object: link.Object, class: class, canon: map[string]float64{}}
		for f, h := range subj.Hours {
			pt.canon[rigsource.CanonicalFilter(f)] += h
		}
		if smp, ok := hmap.Sample(link.Object.RA, link.Object.Dec, link.Object.MajorArcmin/120); ok {
			pt.sample = &smp
		}
		out = append(out, pt)
	}
	return out
}

type stump struct {
	threshold          float64
	correct, n         int
	pos, neg           int
	posAbove, negBelow int
}

func bestSplit(pts []PickRulePoint) (stump, string) {
	vals := make([]float64, 0, len(pts))
	pos := 0
	for _, p := range pts {
		vals = append(vals, p.Rayleigh)
		if p.Shot {
			pos++
		}
	}
	neg := len(pts) - pos
	if pos < MinRuleSide || neg < MinRuleSide {
		return stump{}, fmt.Sprintf("%d with and %d without; a split needs %d of each", pos, neg, MinRuleSide)
	}
	slices.Sort(vals)
	vals = slices.Compact(vals)
	best := make([]stump, 0, len(vals))
	for i := 0; i+1 < len(vals); i++ {
		a, b := vals[i], vals[i+1]
		t := (a + b) / 2
		if a > 0 {
			t = math.Sqrt(a * b)
		}
		st := stump{threshold: t, n: len(pts), pos: pos, neg: neg}
		for _, p := range pts {
			switch {
			case p.Shot && p.Rayleigh >= t:
				st.posAbove++
			case !p.Shot && p.Rayleigh < t:
				st.negBelow++
			}
		}
		st.correct = st.posAbove + st.negBelow
		if len(best) > 0 && st.correct > best[0].correct {
			best = best[:0]
		}
		if len(best) == 0 || st.correct == best[0].correct {
			best = append(best, st)
		}
	}
	if len(best) == 0 || best[0].correct <= max(pos, neg) {
		return stump{}, fmt.Sprintf("H-α does not separate the %d with from the %d without better than always or never adding it", pos, neg)
	}
	return best[(len(best)-1)/2], ""
}

func ceilTenth(v float64) float64 { return math.Ceil(v*10-1e-9) / 10 }

func (s *Service) rule(key string, past []pastTarget, measured *halpha.Sample) PickRule {
	r := PickRule{Key: key, Points: []PickRulePoint{}}
	var population, fallback string
	var fallbackR float64
	fallbackStrict := false
	switch key {
	case RuleSII:
		r.Filter, r.Label = "S", "S-II when H-α is strong"
		population = "emission, remnant and planetary targets you shot H-α on, with and without S-II"
		fallbackR = halpha.ScoreFullR
		fallback = fmt.Sprintf("at or above %s, by the Finder's H-α rule, whose score is full there", rText(halpha.ScoreFullR))
	default:
		r.Filter, r.Label = "H", "H-α when the map shows emission"
		population = "galaxy and cluster targets, with and without H-α"
		fallbackR, fallbackStrict = halpha.ScoreFloorR, true
		fallback = fmt.Sprintf("above %s, by the Finder's H-α rule, which counts nothing at %s or less", rText(halpha.ScoreFloorR), rText(halpha.ScoreFloorR))
	}
	for _, pt := range past {
		if pt.sample == nil {
			continue
		}
		fam := Family(pt.class)
		var in, shot bool
		switch key {
		case RuleSII:
			in, shot = fam == FamilyNarrowband && pt.canon["H"] > 0, pt.canon["S"] > 0
		default:
			in, shot = fam == FamilyBroadband, pt.canon["H"] > 0
		}
		if in {
			r.Points = append(r.Points, PickRulePoint{Name: pt.subject.Name, Object: pt.object.Designation, Rayleigh: pt.sample.Rayleigh, Shot: shot})
		}
	}
	sort.Slice(r.Points, func(i, j int) bool { return r.Points[i].Rayleigh > r.Points[j].Rayleigh })
	st, why := bestSplit(r.Points)
	t := fallbackR
	strict := false
	if why == "" {
		t = ceilTenth(st.threshold)
		r.Derived = true
		r.Basis = fmt.Sprintf("at or above %s: splits your %d %s; %d of %d with %s are at or above, %d of %d without are below",
			rText(t), st.n, population, st.posAbove, st.pos, filterLabel(r.Filter), st.negBelow, st.neg)
	} else {
		strict = fallbackStrict
		r.Basis = fmt.Sprintf("%s; not derived from your targets: of your %s, %s", fallback, population, why)
	}
	r.Threshold, r.Strict = &t, strict
	if measured != nil {
		v := measured.Rayleigh
		r.Measured = &v
		r.Met = v >= t && (!strict || v > t)
	}
	return r
}

type frameExp struct {
	Object   string
	Filter   string
	Exposure float64
	N        int
}

func (s *Service) frameExposures(ctx context.Context, camera string) ([]frameExp, error) {
	var rows []frameExp
	if !s.AppDB.Migrator().HasTable(&app.Frame{}) {
		return rows, nil
	}
	q := s.AppDB.WithContext(ctx).Model(&app.Frame{}).Select("object, filter, exposure, COUNT(*) AS n").
		Where("type = ? AND index_error IS NULL AND exposure > 0", "LIGHT")
	if camera != "" {
		q = q.Where("camera = ?", camera)
	}
	if err := q.Group("object, filter, exposure").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load sub lengths: %w", err)
	}
	return rows, nil
}

func countedMedian(counts map[float64]int) (float64, int) {
	keys := make([]float64, 0, len(counts))
	n := 0
	for k, c := range counts {
		keys = append(keys, k)
		n += c
	}
	if n == 0 {
		return 0, 0
	}
	slices.Sort(keys)
	at := func(i int) float64 {
		for _, k := range keys {
			if i < counts[k] {
				return k
			}
			i -= counts[k]
		}
		return keys[len(keys)-1]
	}
	if n%2 == 1 {
		return at(n / 2), n
	}
	return (at(n/2-1) + at(n/2)) / 2, n
}

func (s *Service) subLength(canon string, rows []frameExp, dustObjects map[string]bool, dust bool) (*float64, int, string) {
	all, onDust := map[float64]int{}, map[float64]int{}
	for _, r := range rows {
		if rigsource.CanonicalFilter(r.Filter) != canon {
			continue
		}
		all[r.Exposure] += r.N
		if dustObjects[r.Object] {
			onDust[r.Exposure] += r.N
		}
	}
	if dust && canon == "L" {
		if m, n := countedMedian(onDust); n > 0 {
			return &m, n, fmt.Sprintf("median of your %d L subs on reflection and dark nebulae", n)
		}
	}
	m, n := countedMedian(all)
	if n == 0 {
		return nil, 0, "no subs in this filter yet"
	}
	basis := fmt.Sprintf("median of your %d %s subs", n, filterLabel(canon))
	if dust && canon == "L" {
		basis += "; none on reflection or dark nebulae yet"
	}
	return &m, n, basis
}

func scaleMatches(m, rig float64) bool {
	return m > 0 && rig > 0 && math.Abs(m/rig-1) <= ScaleTolerance
}

type hAlphaFit struct {
	k, lo, hi float64
	n         int
	why       string
}

func (s *Service) fitHAlpha(past []pastTarget, ms []app.GoalMeasurement, scale float64, snr float64) hAlphaFit {
	byObject := map[string]pastTarget{}
	for _, pt := range past {
		if pt.sample == nil || pt.sample.Rayleigh <= 0 || len(pt.subject.Targets) != 1 {
			continue
		}
		byObject[pt.subject.Targets[0]] = pt
	}
	best := map[string]app.GoalMeasurement{}
	for _, m := range ms {
		if rigsource.CanonicalFilter(m.Filter) != "H" || m.Error != nil || m.LowConfidence || m.SNR <= 0 || m.EffectiveHours <= 0 || !scaleMatches(m.PixelScale, scale) {
			continue
		}
		if _, ok := byObject[m.Object]; !ok {
			continue
		}
		if cur, ok := best[m.Object]; !ok || m.EffectiveHours > cur.EffectiveHours {
			best[m.Object] = m
		}
	}
	var resid []float64
	logs := make([]float64, 0, len(best))
	for obj, m := range best {
		h := m.EffectiveHours * (snr / m.SNR) * (snr / m.SNR)
		logs = append(logs, math.Log10(h)+2*math.Log10(byObject[obj].sample.Rayleigh))
	}
	f := hAlphaFit{n: len(logs)}
	if f.n < MinHAlphaPoints {
		f.why = fmt.Sprintf("%d of your single-frame H-α targets at this pixel scale have a confident SNR and a map value; the fit needs %d", f.n, MinHAlphaPoints)
		return f
	}
	f.k = medianOf(logs)
	for _, l := range logs {
		resid = append(resid, l-f.k)
	}
	f.lo, f.hi = slices.Min(resid), slices.Max(resid)
	return f
}

func (f hAlphaFit) hours(r float64, snr float64) PickHours {
	out := PickHours{Points: f.n}
	if f.why != "" {
		out.Unknown = "unknown: " + f.why
		return out
	}
	if r <= 0 {
		out.Unknown = "unknown: the H-α map shows 0 R here, and hours ∝ R⁻² has no finite answer"
		return out
	}
	h := math.Pow(10, f.k) / (r * r)
	lo, hi := h*math.Pow(10, f.lo), h*math.Pow(10, f.hi)
	out.Hours, out.Low, out.High = ptrf(h), ptrf(lo), ptrf(hi)
	out.Basis = fmt.Sprintf("hours to SNR %g ∝ R⁻² (sky-limited, signal ∝ R), fitted to %d of your H-α targets; they sit %.2g× to %.2g× the fit, so %s to %s",
		snr, f.n, math.Pow(10, f.lo), math.Pow(10, f.hi), hText(lo), hText(hi))
	return out
}

func BandToGaiaG(sbB, bv float64) (float64, bool) {
	if bv <= gaiaBVMin || bv >= gaiaBVMax {
		return 0, false
	}
	gv := -0.04749 - 0.0124*bv - 0.2901*bv*bv + 0.02008*bv*bv*bv
	return sbB + gv - bv, true
}

type skyKey struct{ object, canon string }

func (s *Service) skyByMaster(ctx context.Context) (map[skyKey]float64, error) {
	out := map[skyKey]float64{}
	if !s.AppDB.Migrator().HasTable(&app.SkySample{}) {
		return out, nil
	}
	var rows []struct {
		Object string
		Filter string
		SkyMag float64
	}
	if err := s.AppDB.WithContext(ctx).Model(&app.SkySample{}).Select("object, filter, sky_mag").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load sky samples: %w", err)
	}
	by := map[skyKey][]float64{}
	for _, r := range rows {
		if math.IsNaN(r.SkyMag) || math.IsInf(r.SkyMag, 0) {
			continue
		}
		k := skyKey{r.Object, rigsource.CanonicalFilter(r.Filter)}
		by[k] = append(by[k], r.SkyMag)
	}
	for k, v := range by {
		out[k] = medianOf(v)
	}
	return out, nil
}

func (s *Service) filterSky(ctx context.Context, canon string) skybright.Value {
	site := func(ctx context.Context) (float64, float64, bool) {
		st, err := s.site(ctx)
		if err != nil {
			return 0, 0, false
		}
		return st.Latitude, st.Longitude, true
	}
	v, err := skybright.MeasureIn(ctx, s.AppDB, site, s.now(), canon)
	if err != nil {
		return skybright.None(err.Error())
	}
	return v
}

func (s *Service) depthHours(o catalog.Object, canon string, ms []app.GoalMeasurement, sky skybright.Value, skies map[skyKey]float64, scale, snr float64) PickHours {
	out := PickHours{}
	if o.SurfaceBrightness == nil || o.SurfaceBrightnessSource != sbSourceNGC {
		out.Unknown = "unknown: no OpenNGC surface brightness catalogued"
		return out
	}
	sbB := *o.SurfaceBrightness
	if o.BMinusV == nil {
		out.Unknown = fmt.Sprintf("unknown: surface brightness %.1f mag/arcsec² is in B (OpenNGC) and the sky is in Gaia G; no B−V is catalogued to convert it", sbB)
		return out
	}
	bv := *o.BMinusV
	sbG, ok := BandToGaiaG(sbB, bv)
	if !ok {
		out.Unknown = fmt.Sprintf("unknown: B−V %.2f is outside the %g to %g range of the Gaia B-to-G relation", bv, gaiaBVMin, gaiaBVMax)
		return out
	}
	if sky.Mag == nil || sky.Basis.Source != skybright.SourceMeasured {
		why := "not measured"
		if sky.Basis.Reason != nil {
			why = *sky.Basis.Reason
		}
		out.Unknown = fmt.Sprintf("unknown: the sky brightness in %s is not measured (%s)", filterLabel(canon), why)
		return out
	}
	now := *sky.Mag
	var d1 []float64
	for _, m := range ms {
		if rigsource.CanonicalFilter(m.Filter) != canon || m.Error != nil || m.Depth == nil || m.EffectiveHours <= 0 ||
			(m.DepthSystem != "" && m.DepthSystem != goals.SystemGaiaG) || !scaleMatches(m.PixelScale, scale) {
			continue
		}
		skyM, ok := skies[skyKey{m.Object, canon}]
		if !ok {
			continue
		}
		d1 = append(d1, *m.Depth-1.25*math.Log10(m.EffectiveHours)+0.5*(now-skyM))
	}
	out.Points = len(d1)
	if len(d1) == 0 {
		out.Unknown = fmt.Sprintf("unknown: no %s master at this pixel scale has a Gaia G depth and a measured sky yet", filterLabel(canon))
		return out
	}
	need := sbG + 2.5*math.Log10(snr/goals.DepthSNR)
	hoursAt := func(d float64) float64 { return math.Pow(10, (need-d)/1.25) }
	med, lo, hi := medianOf(d1), slices.Min(d1), slices.Max(d1)
	h := hoursAt(med)
	out.Hours, out.Low, out.High = ptrf(h), ptrf(hoursAt(hi)), ptrf(hoursAt(lo))
	out.Basis = fmt.Sprintf("SNR %g at the catalogued mean surface brightness %.1f mag/arcsec² B (OpenNGC), %.1f in Gaia G with B−V %.2f (%s); "+
		"your %s depth at SNR %g after 1 h, scaled to the %.2f mag/arcsec² Gaia G sky measured in %s over %d nights, is %.2f (%.2f to %.2f) from %d masters; depth grows 1.25 mag per decade of hours, so %s to %s",
		snr, sbB, sbG, bv, gaiaRelation, filterLabel(canon), goals.DepthSNR, now, filterLabel(canon), sky.Basis.Nights, med, lo, hi, len(d1), hText(hoursAt(hi)), hText(hoursAt(lo)))
	return out
}

func templateFor(snap *planning.Snapshot, canon string) (*PickTemplate, string) {
	var best *planning.Template
	for i, t := range snap.Templates {
		if rigsource.CanonicalFilter(t.Filter) != canon {
			continue
		}
		if best == nil || t.UsedByPlans > best.UsedByPlans || (t.UsedByPlans == best.UsedByPlans && t.Name < best.Name) {
			best = &snap.Templates[i]
		}
	}
	if best == nil {
		return nil, fmt.Sprintf("no exposure template for %s", filterLabel(canon))
	}
	return &PickTemplate{ID: best.ID, Name: best.Name, Filter: best.Filter}, fmt.Sprintf("your %s template used by the most plans (%d)", filterLabel(canon), best.UsedByPlans)
}

func matchSet(snap *planning.Snapshot, canons []string) (*planning.ExposureSet, map[string]planning.Template) {
	byName := map[string]planning.Template{}
	for _, t := range snap.Templates {
		byName[strings.ToLower(strings.TrimSpace(t.Name))] = t
	}
	for i, set := range snap.Sets {
		got := map[string]planning.Template{}
		ok := len(set.Items) == len(canons)
		for _, it := range set.Items {
			t, found := byName[strings.ToLower(strings.TrimSpace(it.Template))]
			c := rigsource.CanonicalFilter(t.Filter)
			if !found || !slices.Contains(canons, c) {
				ok = false
				break
			}
			if _, dup := got[c]; dup {
				ok = false
				break
			}
			got[c] = t
		}
		if ok && len(got) == len(canons) {
			return &snap.Sets[i], got
		}
	}
	return nil, nil
}

type hoursInputs struct {
	o          catalog.Object
	class, fam string
	canon      string
	ha         *halpha.Sample
	fit        hAlphaFit
	ms         []app.GoalMeasurement
	skies      map[skyKey]float64
	scale, snr float64
}

func (s *Service) filterHours(ctx context.Context, in hoursInputs) PickHours {
	switch {
	case in.canon == "H" && in.fam == FamilyBroadband:
		return PickHours{Unknown: "unknown, the goal model will measure it: the map's H-α here is Milky Way foreground at 6′, not this object's own emission"}
	case in.canon == "H" && in.ha == nil:
		return PickHours{Unknown: "unknown: the H-α map has no value here"}
	case in.canon == "H":
		return in.fit.hours(in.ha.Rayleigh, in.snr)
	case in.fam == FamilyBroadband && in.class == ClassGalaxy:
		return s.depthHours(in.o, in.canon, in.ms, s.filterSky(ctx, in.canon), in.skies, in.scale, in.snr)
	case in.fam == FamilyBroadband:
		return PickHours{Unknown: "unknown: no surface brightness is catalogued for clusters"}
	default:
		return PickHours{Unknown: "unknown, the goal model will measure it"}
	}
}

func (s *Service) choosePalette(out *ExposurePick, fam string, past []pastTarget, haText string) []string {
	broad := []string{"L", "R", "G", "B"}
	var canons []string
	var key, extra string
	switch fam {
	case FamilyNarrowband:
		out.Palette, canons, key, extra = PaletteHOO, []string{"H", "O"}, RuleSII, "S"
	case FamilyBroadband:
		out.Palette, canons, key, extra = PaletteLRGB, broad, RuleHa, "H"
	default:
		out.Palette = PaletteLRGB
		out.Reason = fmt.Sprintf("%s: %s", PaletteLRGB, out.ClassLabel)
		return broad
	}
	r := s.rule(key, past, out.HAlpha)
	out.Rules = append(out.Rules, r)
	base := out.Palette
	out.Reason = fmt.Sprintf("%s: %s, %s", base, out.ClassLabel, haText)
	switch {
	case r.Met:
		canons = append(canons, extra)
		out.Palette += " + " + filterLabel(extra)
		word := "at or above"
		if r.Strict {
			word = "above"
		}
		out.Reason = fmt.Sprintf("%s: %s, %s %s %s", out.Palette, out.ClassLabel, haText, word, rText(*r.Threshold))
	case r.Measured != nil:
		word := "below"
		if r.Strict {
			word = "at or below"
		}
		out.Reason += fmt.Sprintf(", %s the %s %s rule", word, rText(*r.Threshold), filterLabel(extra))
	}
	return canons
}

func (s *Service) Pick(ctx context.Context, id string, plans *planning.Snapshot) (ExposurePick, error) {
	o, ok := s.Catalog.Get(id)
	if !ok {
		return ExposurePick{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return ExposurePick{}, err
	}
	hmap, hst := s.halphaMap()
	_, info := s.rig(ctx)
	out := ExposurePick{Object: o, HAlphaMap: hst, Rules: []PickRule{}, Filters: []PickFilter{}, Missing: []string{}, GoalSNR: goals.DefaultSNRGoal, Value: s.skyValue(ctx)}
	out.Class, out.ClassLabel = PickClass(o.Type)
	if smp, ok := hmap.Sample(o.RA, o.Dec, o.MajorArcmin/120); ok {
		out.HAlpha = &smp
	}
	fam := Family(out.Class)
	if fam == "" {
		out.NoPick = fmt.Sprintf("no filter rule for %s objects; choose a set", o.Type)
		return out, nil
	}
	if info.Colour != nil && *info.Colour {
		out.NoPick = "the rig is a colour camera, so there are no filters to pick"
		return out, nil
	}
	past := s.pastTargets(snap, hmap)
	haText := "H-α not on the map"
	if hmap == nil {
		haText = "H-α map " + hst.State
	}
	if out.HAlpha != nil {
		haText = "H-α " + rText(out.HAlpha.Rayleigh)
	}
	canons := s.choosePalette(&out, fam, past, haText)
	camera := ""
	if info.Basis.Camera != nil {
		camera = *info.Basis.Camera
	}
	rows, err := s.frameExposures(ctx, camera)
	if err != nil {
		return out, err
	}
	dustObjects := map[string]bool{}
	for _, pt := range past {
		if Family(pt.class) == FamilyDust {
			for _, t := range pt.subject.Targets {
				dustObjects[t] = true
			}
		}
	}
	var ms []app.GoalMeasurement
	if s.AppDB.Migrator().HasTable(&app.GoalMeasurement{}) {
		if err := s.AppDB.WithContext(ctx).Model(&app.GoalMeasurement{}).
			Select("object, filter, snr, effective_hours, depth, depth_system, pixel_scale, low_confidence").
			Where("error IS NULL").Find(&ms).Error; err != nil {
			return out, fmt.Errorf("load goal measurements: %w", err)
		}
	}
	skies, err := s.skyByMaster(ctx)
	if err != nil {
		return out, err
	}
	scale := 0.0
	if info.Scale != nil {
		scale = *info.Scale
	}
	fit := s.fitHAlpha(past, ms, scale, out.GoalSNR)
	var set *planning.ExposureSet
	var setTemplates map[string]planning.Template
	if plans != nil {
		set, setTemplates = matchSet(plans, canons)
		if set != nil {
			out.Set = &PickSet{ID: set.ID, Name: set.Name, Projects: set.Projects}
		}
	}
	for _, c := range canons {
		pf := PickFilter{Filter: filterLabel(c)}
		switch {
		case set != nil:
			t := setTemplates[c]
			pf.Template = &PickTemplate{ID: t.ID, Name: t.Name, Filter: t.Filter}
			pf.TemplateBasis = fmt.Sprintf("from your set %s, used on %d projects", set.Name, set.Projects)
		case plans != nil:
			pf.Template, pf.TemplateBasis = templateFor(plans, c)
		default:
			pf.TemplateBasis = "the scheduler's templates are not loaded"
		}
		if pf.Template == nil {
			out.Missing = append(out.Missing, pf.Filter)
		}
		pf.Exposure, pf.Subs, pf.ExposureBasis = s.subLength(c, rows, dustObjects, fam == FamilyDust)
		pf.Hours = s.filterHours(ctx, hoursInputs{o: o, class: out.Class, fam: fam, canon: c, ha: out.HAlpha, fit: fit, ms: ms, skies: skies, scale: scale, snr: out.GoalSNR})
		out.Filters = append(out.Filters, pf)
	}
	return out, nil
}
