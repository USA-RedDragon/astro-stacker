package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/store/tables"
	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
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
	SIISignificance = 0.05
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
	Goal    string   `json:"goal"`
	Basis   string   `json:"basis"`
	Unknown string   `json:"unknown,omitempty"`
}

type PickTemplate struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Filter string `json:"filter"`
}

type PickLength struct {
	Gain     int     `json:"gain"`
	Bucket   string  `json:"bucket"`
	Exposure float64 `json:"exposure"`
	Basis    string  `json:"basis"`
}

type PickFilter struct {
	Filter        string        `json:"filter"`
	Template      *PickTemplate `json:"template"`
	TemplateBasis string        `json:"templateBasis"`
	Exposure      *float64      `json:"exposure"`
	ExposureBasis string        `json:"exposureBasis"`
	Automatic     bool          `json:"automatic"`
	Lengths       []PickLength  `json:"lengths"`
	LengthBasis   string        `json:"lengthBasis"`
	Subs          int           `json:"subs"`
	Hours         PickHours     `json:"hours"`
}

type PickSet struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Projects int    `json:"projects"`
}

type PickGoal struct {
	Kind  goals.Kind `json:"kind"`
	SNR   float64    `json:"snr"`
	Depth float64    `json:"depth"`
}

func DepthClass(class string) bool {
	switch class {
	case ClassGalaxy, ClassCluster, ClassReflection, ClassDark:
		return true
	}
	return false
}

func withArticle(label string) string {
	if label != "" && strings.ContainsRune("aeiou", rune(label[0])) {
		return "an " + label
	}
	return "a " + label
}

func DefaultGoalKind(class, label string) (goals.Kind, string) {
	if DepthClass(class) {
		return goals.KindDepth, fmt.Sprintf("depth by default for %s: its broadband light is set by depth, not by one faint band", withArticle(label))
	}
	if class == "" {
		return goals.KindSNR, "faint-signal SNR by default: no class for this object"
	}
	return goals.KindSNR, fmt.Sprintf("faint-signal SNR by default for %s", withArticle(label))
}

func (g PickGoal) normal(class, label string) (PickGoal, string) {
	basis := "chosen"
	if g.Kind != goals.KindDepth && g.Kind != goals.KindSNR {
		g.Kind, basis = DefaultGoalKind(class, label)
	}
	if !(g.SNR > 0) {
		g.SNR = goals.DefaultSNRGoal
	}
	if !(g.Depth > 0) {
		g.Depth = 0
	}
	return g, basis
}

type ExposurePick struct {
	Object     catalog.Object `json:"object"`
	Class      string         `json:"class"`
	ClassLabel string         `json:"classLabel"`
	ClassBasis string         `json:"classBasis"`
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
	Goal       PickGoal       `json:"goal"`
	GoalBasis  string         `json:"goalBasis"`
	skybright.Value
}

func PickClass(t string) (class, label string) {
	switch t {
	case catalog.TypeEmission:
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
	switch {
	case h >= 10:
		return fmt.Sprintf("%.0f h", h)
	case h >= 0.1:
		return fmt.Sprintf("%.1f h", h)
	}
	return fmt.Sprintf("%.2g h", h)
}

type pastTarget struct {
	subject Subject
	object  catalog.Object
	class   string
	sample  *halpha.Sample
	canon   map[string]float64
	own     bool
}

func ownProject(subj Subject) bool {
	return subj.Kind == SubjectProject && subj.ProjectID > 0
}

func canonHours(subj Subject) map[string]float64 {
	out := map[string]float64{}
	for f, h := range subj.Hours {
		out[rigsource.CanonicalFilter(f)] += h
	}
	return out
}

func namedSHO(name string) bool {
	return slices.ContainsFunc(strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), func(w string) bool { return strings.EqualFold(w, "SHO") })
}

func ownHistory(snap *snapshot, id string) []Subject {
	keys := map[string]bool{}
	for _, l := range snap.byObject[id] {
		if l.Linked() {
			keys[l.Subject] = true
		}
	}
	var out []Subject
	for _, subj := range snap.subjects {
		if keys[subj.Key] && ownProject(subj) {
			out = append(out, subj)
		}
	}
	return out
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
		pt := pastTarget{subject: subj, object: link.Object, class: class, canon: canonHours(subj), own: ownProject(subj)}
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
		population = "own scheduler projects on emission, remnant and planetary targets shot in H-α, with and without S-II"
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
			in, shot = pt.own && fam == FamilyNarrowband && pt.canon["H"] > 0, pt.canon["S"] > 0
		default:
			in, shot = fam == FamilyBroadband, pt.canon["H"] > 0
		}
		if in {
			r.Points = append(r.Points, PickRulePoint{Name: pt.subject.Name, Object: pt.object.Designation, Rayleigh: pt.sample.Rayleigh, Shot: shot})
		}
	}
	sort.Slice(r.Points, func(i, j int) bool { return r.Points[i].Rayleigh > r.Points[j].Rayleigh })
	st, why := bestSplit(r.Points)
	if why == "" && key == RuleSII {
		why = siiChance(st)
	}
	if measured != nil {
		v := measured.Rayleigh
		r.Measured = &v
	}
	if why != "" && key == RuleSII {
		r.Basis = fmt.Sprintf("no H-α threshold for S-II: of your %s, %s", population, why)
		return r
	}
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
	if r.Measured != nil {
		r.Met = *r.Measured >= t && (!strict || *r.Measured > t)
	}
	return r
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// fisherUpper is the one-sided Fisher exact p-value: the chance that a random
// draw of above targets from n, pos of them positive, holds posAbove or more.
func fisherUpper(n, pos, above, posAbove int) float64 {
	p := 0.0
	for k := posAbove; k <= min(pos, above); k++ {
		if above-k > n-pos {
			continue
		}
		p += math.Exp(logChoose(pos, k) + logChoose(n-pos, above-k) - logChoose(n, above))
	}
	return min(p, 1)
}

func siiChance(st stump) string {
	above := st.posAbove + st.neg - st.negBelow
	p := fisherUpper(st.n, st.pos, above, st.posAbove)
	if p <= SIISignificance {
		return ""
	}
	return fmt.Sprintf("the best split, at %s, has %d of %d with S-II at or above and %d of %d without below; chance alone gives a split that good %.0f%% of the time (one-sided Fisher exact test), more than the %.0f%% allowed",
		rText(ceilTenth(st.threshold)), st.posAbove, st.pos, st.negBelow, st.neg, p*100, SIISignificance*100)
}

type frameExp struct {
	Object   string
	Filter   string
	Exposure float64
	N        int
}

func (s *Service) frameExposures(ctx context.Context, camera string) ([]frameExp, error) {
	var rows []frameExp
	if has, err := tables.Has(ctx, s.AppDB, &app.Frame{}); err != nil || !has {
		return rows, err
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
	k, lo, hi       float64
	looLo, looHi    float64
	looFit, looNull float64
	n               int
	why             string
}

func leaveOneOut(v []float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		rest := make([]float64, 0, len(v)-1)
		rest = append(rest, v[:i]...)
		rest = append(rest, v[i+1:]...)
		out[i] = v[i] - medianOf(rest)
	}
	return out
}

func medianAbs(v []float64) float64 {
	a := make([]float64, len(v))
	for i, x := range v {
		a[i] = math.Abs(x)
	}
	return medianOf(a)
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
	objs := make([]string, 0, len(best))
	for obj := range best {
		objs = append(objs, obj)
	}
	slices.Sort(objs)
	logs := make([]float64, 0, len(objs))
	plain := make([]float64, 0, len(objs))
	for _, obj := range objs {
		m := best[obj]
		lh := math.Log10(m.EffectiveHours * (snr / m.SNR) * (snr / m.SNR))
		plain = append(plain, lh)
		logs = append(logs, lh+2*math.Log10(byObject[obj].sample.Rayleigh))
	}
	f := hAlphaFit{n: len(logs)}
	if f.n < MinHAlphaPoints {
		f.why = fmt.Sprintf("%d of your single-frame H-α targets at this pixel scale have a confident SNR and a map value; the fit needs %d", f.n, MinHAlphaPoints)
		return f
	}
	f.k = medianOf(logs)
	resid := make([]float64, 0, len(logs))
	for _, l := range logs {
		resid = append(resid, l-f.k)
	}
	f.lo, f.hi = slices.Min(resid), slices.Max(resid)
	loo := leaveOneOut(logs)
	f.looLo, f.looHi = slices.Min(loo), slices.Max(loo)
	f.looFit, f.looNull = medianAbs(loo), medianAbs(leaveOneOut(plain))
	if f.looFit >= f.looNull {
		f.why = fmt.Sprintf("your %d H-α targets don't follow H-α brightness closely enough to predict hours (they sit %.2g× to %.2g× the fit; "+
			"left out one at a time, the fit misses their hours by a median %.2g×, and ignoring H-α misses by %.2g×)",
			f.n, math.Pow(10, f.lo), math.Pow(10, f.hi), math.Pow(10, f.looFit), math.Pow(10, f.looNull))
	}
	return f
}

func (f hAlphaFit) hours(r float64, snr float64) PickHours {
	out := PickHours{Points: f.n, Goal: fmt.Sprintf("SNR %g", snr)}
	if f.why != "" {
		out.Unknown = "unknown: " + f.why
		return out
	}
	if r <= 0 {
		out.Unknown = "unknown: the H-α map shows 0 R here, and hours ∝ R⁻² has no finite answer"
		return out
	}
	h := math.Pow(10, f.k) / (r * r)
	lo, hi := h*math.Pow(10, f.looLo), h*math.Pow(10, f.looHi)
	out.Hours, out.Low, out.High = ptrf(h), ptrf(lo), ptrf(hi)
	out.Basis = fmt.Sprintf("hours to SNR %g ∝ R⁻² (sky-limited, signal ∝ R) at the %s on the map here, fitted to %d of your H-α targets; "+
		"left out one at a time, the fit misses their hours by a median %.2g× (ignoring H-α misses by %.2g×) and by %.2g× to %.2g× at worst, so %s to %s",
		snr, rText(r), f.n, math.Pow(10, f.looFit), math.Pow(10, f.looNull), math.Pow(10, f.looLo), math.Pow(10, f.looHi), hText(lo), hText(hi))
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
	if has, err := tables.Has(ctx, s.AppDB, &app.SkySample{}); err != nil || !has {
		return out, err
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

func meanSBContext(o catalog.Object, goal float64) string {
	if o.SurfaceBrightness == nil || o.SurfaceBrightnessSource != sbSourceNGC {
		return "no OpenNGC mean surface brightness is catalogued to compare with the goal"
	}
	sbB := *o.SurfaceBrightness
	if o.BMinusV == nil {
		return fmt.Sprintf("the catalogued mean surface brightness is %.1f mag/arcsec² in B (OpenNGC), and no B−V is catalogued to put it in Gaia G beside the goal", sbB)
	}
	bv := *o.BMinusV
	sbG, ok := BandToGaiaG(sbB, bv)
	if !ok {
		return fmt.Sprintf("the catalogued mean surface brightness is %.1f mag/arcsec² in B (OpenNGC), and its B−V %.2f is outside the %g to %g range of the Gaia B-to-G relation, so it is not compared with the goal", sbB, bv, gaiaBVMin, gaiaBVMax)
	}
	verdict := fmt.Sprintf("%.1f mag brighter than the goal, so its mean brightness is above the goal depth", goal-sbG)
	if sbG > goal {
		verdict = fmt.Sprintf("%.1f mag fainter than the goal, so its mean brightness is below the goal depth", sbG-goal)
	}
	return fmt.Sprintf("the catalogued mean surface brightness %.1f mag/arcsec² B (OpenNGC) is %.1f in Gaia G with B−V %.2f (%s), %s",
		sbB, sbG, bv, gaiaRelation, verdict)
}

func (s *Service) depthHours(o catalog.Object, canon, goalFilter string, typed float64, ms []app.GoalMeasurement, sky skybright.Value, skies map[skyKey]float64, scale float64) PickHours {
	goal := goals.DefaultDepth(frameheader.NormalizeFilter(goalFilter))
	goalWhy := fmt.Sprintf("the depth goal a new target gets for %s when its goal is depth and no depth is typed (the stacker's default, the same value a project's Goal tab sets when you switch it to depth; a set value, not fitted to your data)", filterLabel(canon))
	if typed > 0 {
		goal, goalWhy = typed, "the depth you typed for every filter"
	}
	out := PickHours{Goal: fmt.Sprintf("%.1f mag/arcsec² at SNR %g", goal, goals.DepthSNR)}
	sbNote := meanSBContext(o, goal)
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
	hoursAt := func(d float64) float64 { return math.Pow(10, (goal-d)/1.25) }
	masters := "masters"
	if len(d1) == 1 {
		masters = "master"
	}
	med, lo, hi := medianOf(d1), slices.Min(d1), slices.Max(d1)
	out.Hours, out.Low, out.High = ptrf(hoursAt(med)), ptrf(hoursAt(hi)), ptrf(hoursAt(lo))
	out.Basis = fmt.Sprintf("hours to the %.1f mag/arcsec² Gaia G depth goal at SNR %g, %s; "+
		"your %s depth at SNR %g after 1 h, scaled to the %.2f mag/arcsec² Gaia G sky measured in %s over %d nights, is %.2f (%.2f to %.2f) from %d %s; depth grows 1.25 mag per decade of hours, so %s to %s; "+
		"with the plateau stop on, the goal also ends when a master gains under %g%% per hour, which this does not predict; %s",
		goal, goals.DepthSNR, goalWhy, filterLabel(canon), goals.DepthSNR, now, filterLabel(canon), sky.Basis.Nights, med, lo, hi, len(d1), masters,
		hText(hoursAt(hi)), hText(hoursAt(lo)), goals.PlateauGainPct, sbNote)
	return out
}

func templateFor(snap *planning.Snapshot, canon string) (*PickTemplate, string) {
	t, ok := pickTemplate(snap, canon)
	if !ok {
		return nil, fmt.Sprintf("no exposure template for %s", filterLabel(canon))
	}
	return &PickTemplate{ID: t.ID, Name: t.Name, Filter: t.Filter}, fmt.Sprintf("your %s filter", t.Filter)
}

// pickTemplate is the template a plan on the canonical filter uses: after
// the template migration the planning filter resolution of the filter with
// the most used template, never a retired one; before it the most used
// template that is not retired.
func pickTemplate(snap *planning.Snapshot, canon string) (planning.Template, bool) {
	profile := snap.FilterProfile()
	if snap.HasManaged(profile) {
		filter, most := "", -1
		for _, f := range snap.Filters {
			if rigsource.CanonicalFilter(f.Name) != canon {
				continue
			}
			if n := snap.MostPlans(profile, f.Name); n > most {
				filter, most = f.Name, n
			}
		}
		t, ok := snap.ResolveFilter(profile, filter)
		if !ok || t.Retired {
			return planning.Template{}, false
		}
		return t, true
	}
	var best *planning.Template
	for i, t := range snap.Templates {
		if t.Retired || rigsource.CanonicalFilter(t.Filter) != canon {
			continue
		}
		if best == nil || t.UsedByPlans > best.UsedByPlans || (t.UsedByPlans == best.UsedByPlans && t.Name < best.Name) {
			best = &snap.Templates[i]
		}
	}
	if best == nil {
		return planning.Template{}, false
	}
	return *best, true
}

func matchSet(snap *planning.Snapshot, canons []string) (*planning.ExposureSet, map[string]planning.Template) {
	byName := map[string]planning.Template{}
	for _, t := range snap.AllTemplates() {
		byName[strings.ToLower(strings.TrimSpace(t.Name))] = t
	}
	for i, set := range snap.Sets {
		got := map[string]planning.Template{}
		ok := len(set.Items) == len(canons)
		for _, it := range set.Items {
			t, found := byName[strings.ToLower(strings.TrimSpace(it.Template))]
			c := rigsource.CanonicalFilter(t.Filter)
			if !found || t.Retired || !slices.Contains(canons, c) {
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
	goalFilter string
	ha         *halpha.Sample
	fit        hAlphaFit
	ms         []app.GoalMeasurement
	skies      map[skyKey]float64
	scale      float64
	goal       PickGoal
}

func (s *Service) filterHours(ctx context.Context, in hoursInputs) PickHours {
	if in.goal.Kind == goals.KindDepth {
		return s.depthGoalHours(ctx, in)
	}
	snrGoal := fmt.Sprintf("SNR %g", in.goal.SNR)
	switch {
	case in.canon == "H" && in.fam == FamilyBroadband:
		return PickHours{Goal: snrGoal, Unknown: "unknown, the goal model will measure it: the map's H-α here is Milky Way foreground at 6′, not this object's own emission"}
	case in.canon == "H" && in.ha == nil:
		return PickHours{Goal: snrGoal, Unknown: "unknown: the H-α map has no value here"}
	case in.canon == "H":
		return in.fit.hours(in.ha.Rayleigh, in.goal.SNR)
	case in.fam == FamilyBroadband:
		return PickHours{Goal: snrGoal, Unknown: "unknown, the goal model will measure it: the SNR goal is set in this object's own faint band, and nothing catalogued predicts that signal"}
	default:
		return PickHours{Goal: snrGoal, Unknown: "unknown, the goal model will measure it"}
	}
}

func (s *Service) depthGoalHours(ctx context.Context, in hoursInputs) PickHours {
	if skybright.Broadband(in.canon) {
		return s.depthHours(in.o, in.canon, in.goalFilter, in.goal.Depth, in.ms, s.filterSky(ctx, in.canon), in.skies, in.scale)
	}
	goal := goals.DefaultDepth(frameheader.NormalizeFilter(in.goalFilter))
	if in.goal.Depth > 0 {
		goal = in.goal.Depth
	}
	return PickHours{Goal: fmt.Sprintf("%.1f mag/arcsec² at SNR %g", goal, goals.DepthSNR),
		Unknown: fmt.Sprintf("unknown, the goal model will measure it: the depth model scales by the sky measured in the same filter, and only L, R, G and B sky is measured, not %s", filterLabel(in.canon))}
}

// siiChoice decides S-II for a narrowband target. HOO is the default; S-II
// needs the user's own projects to ask for it: one named for SHO, S-II
// already shot on this object, or a significant H-α rule from their projects.
func siiChoice(r PickRule, hist []Subject, haText string) (bool, string) {
	for _, subj := range hist {
		if namedSHO(subj.Name) {
			return true, fmt.Sprintf("%s; your project %s is named for SHO", haText, subj.Name)
		}
		for _, n := range subj.Targets {
			if namedSHO(n) {
				return true, fmt.Sprintf("%s; your project %s has the target %s, named for SHO", haText, subj.Name, n)
			}
		}
	}
	var without []string
	for _, subj := range hist {
		c := canonHours(subj)
		if c["H"] <= 0 {
			continue
		}
		if c["S"] > 0 {
			return true, fmt.Sprintf("%s; you added S-II on your project %s (%s of S-II)", haText, subj.Name, hText(c["S"]))
		}
		without = append(without, subj.Name)
	}
	if len(without) > 0 {
		if len(without) == 1 {
			return false, fmt.Sprintf("%s; you shot your project %s in H-α without S-II", haText, without[0])
		}
		return false, fmt.Sprintf("%s; you shot your projects %s and %s in H-α without S-II", haText, strings.Join(without[:len(without)-1], ", "), without[len(without)-1])
	}
	switch {
	case r.Met:
		word := "at or above"
		if r.Strict {
			word = "above"
		}
		return true, fmt.Sprintf("%s %s %s, the S-II rule learned from your own projects", haText, word, rText(*r.Threshold))
	case r.Threshold == nil:
		return false, haText + "; S-II only when your own projects call for it, and they don't yet"
	case r.Measured != nil:
		return false, fmt.Sprintf("%s, below the %s S-II rule", haText, rText(*r.Threshold))
	}
	return false, haText
}

func (s *Service) choosePalette(out *ExposurePick, fam string, past []pastTarget, hist []Subject, haText string) []string {
	broad := []string{"L", "R", "G", "B"}
	var canons []string
	var key, extra string
	switch fam {
	case FamilyNarrowband:
		r := s.rule(RuleSII, past, out.HAlpha)
		out.Rules = append(out.Rules, r)
		out.Palette, canons = PaletteHOO, []string{"H", "O"}
		add, why := siiChoice(r, hist, haText)
		if add {
			out.Palette += " + " + filterLabel("S")
			canons = append(canons, "S")
		}
		out.Reason = fmt.Sprintf("%s: %s, %s", out.Palette, out.ClassLabel, why)
		return canons
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

func (s *Service) Pick(ctx context.Context, id string, plans *planning.Snapshot, goal PickGoal) (ExposurePick, error) {
	o, ok := s.Catalog.Get(id)
	if !ok {
		return ExposurePick{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	class, label := PickClass(o.Type)
	goal, goalBasis := goal.normal(class, label)
	snap, err := s.snapshot(ctx)
	if err != nil {
		return ExposurePick{}, err
	}
	hmap, hst := s.halphaMap()
	_, info := s.rig(ctx)
	out := ExposurePick{Object: o, HAlphaMap: hst, Rules: []PickRule{}, Filters: []PickFilter{}, Missing: []string{}, GoalSNR: goal.SNR, Goal: goal, GoalBasis: goalBasis, Value: s.skyValue(ctx)}
	out.Class, out.ClassLabel = class, label
	if o.TypeBasis != "" {
		out.ClassBasis = out.ClassLabel + ": " + o.TypeBasis
	}
	if smp, ok := hmap.Sample(o.RA, o.Dec, o.MajorArcmin/120); ok {
		out.HAlpha = &smp
	}
	fam := Family(out.Class)
	if o.Type == catalog.TypeNebula {
		out.Reason = out.ClassBasis
		out.NoPick = "the catalogue does not say whether it is an emission or a reflection nebula; choose a set"
		return out, nil
	}
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
	canons := s.choosePalette(&out, fam, past, ownHistory(snap, o.ID), haText)
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
	hasMs, err := tables.Has(ctx, s.AppDB, &app.GoalMeasurement{})
	if err != nil {
		return out, err
	}
	if hasMs {
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
		s.automaticLength(&pf, c, plans)
		goalFilter := c
		if pf.Template != nil {
			goalFilter = pf.Template.Filter
		}
		pf.Hours = s.filterHours(ctx, hoursInputs{o: o, class: out.Class, fam: fam, canon: c, goalFilter: goalFilter, ha: out.HAlpha, fit: fit, ms: ms, skies: skies, scale: scale, goal: goal})
		out.Filters = append(out.Filters, pf)
	}
	return out, nil
}

func (s *Service) automaticLength(pf *PickFilter, canon string, plans *planning.Snapshot) {
	pf.Lengths = []PickLength{}
	var gain *int
	if pf.Template != nil && plans != nil {
		for _, t := range plans.AllTemplates() {
			if t.ID == pf.Template.ID {
				gain = t.Gain
			}
		}
	}
	var res *sublength.Result
	if s.SubLengths != nil {
		res = s.SubLengths()
	}
	rows := res.SiteLengths(canon, gain)
	if gain == nil && len(rows) > 0 {
		g := rows[0].Gain
		gain = &g
		rows = res.SiteLengths(canon, gain)
	}
	var parts []string
	for _, r := range rows {
		if !(r.Exposure > 0) {
			continue
		}
		pf.Lengths = append(pf.Lengths, PickLength{Gain: r.Gain, Bucket: string(r.Bucket), Exposure: r.Exposure, Basis: r.Basis})
		parts = append(parts, fmt.Sprintf("%s %g s", r.Bucket, r.Exposure))
	}
	pf.Automatic = res != nil && res.Mode == sublength.ModeOn
	switch {
	case len(parts) == 0:
		pf.LengthBasis = "Picked per moon glare once the sky in this filter is measured; not measured yet, so the template's length is used until then."
	case pf.Automatic:
		pf.LengthBasis = fmt.Sprintf("Chosen automatically per moon glare from the sky measured at this site: %s. Once the target has its own subs, its own sky is used.", strings.Join(parts, ", "))
	default:
		pf.LengthBasis = fmt.Sprintf("Would be chosen per moon glare from the sky measured at this site: %s. Publishing the lengths to the scheduler is off, so the template's length is used.", strings.Join(parts, ", "))
	}
}
