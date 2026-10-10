package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	CriterionPass = "pass"
	CriterionFail = "fail"
	CriterionOpen = "open"
	CriterionNone = "none"

	ScopeRig     = "rig"
	ScopeTonight = "tonight"

	VerdictFits      = "fits"
	VerdictNoFit     = "no"
	VerdictUnchecked = "unchecked"

	colourFilter = "OSC"

	monthSampleText = "one sampled night per month, the night of the 15th, counting hours in astronomical darkness; the Moon is ignored"
)

type CollabSource interface {
	State() starfront.State
}

type Criterion struct {
	Name   string `json:"name"`
	Rule   string `json:"rule"`
	You    string `json:"you"`
	Result string `json:"result"`
	Scope  string `json:"scope"`
}

type Have struct {
	Subject   string             `json:"subject"`
	Name      string             `json:"name"`
	ProjectID int                `json:"projectId,omitempty"`
	Hours     map[string]float64 `json:"hours"`
	LastNight *time.Time         `json:"lastNight,omitempty"`
	Subs      int                `json:"subs"`
}

type Collab struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Coordinator string              `json:"coordinator"`
	Status      string              `json:"status"`
	Kind        string              `json:"kind"`
	Created     time.Time           `json:"created"`
	Notes       string              `json:"notes,omitempty"`
	Region      starfront.Region    `json:"region"`
	Near        string              `json:"near,omitempty"`
	Goals       map[string]float64  `json:"goals"`
	Progress    map[string]float64  `json:"progress"`
	Summary     *starfront.Summary  `json:"summary,omitempty"`
	Fits        bool                `json:"fits"`
	Verdict     string              `json:"verdict"`
	Unchecked   int                 `json:"unchecked"`
	Criteria    []Criterion         `json:"criteria"`
	Panels      int                 `json:"panels"`
	Columns     int                 `json:"columns"`
	Rows        int                 `json:"rows"`
	Coverage    *float64            `json:"coverage"`
	Tonight     *Tonight            `json:"tonight,omitempty"`
	Curve       []sky.AltitudePoint `json:"curve,omitempty"`
	Months      [12]float64         `json:"months"`
	BestMonths  []int               `json:"bestMonths"`
	Have        []Have              `json:"have"`
}

type CollabsView struct {
	Enabled   bool                  `json:"enabled"`
	SourceURL string                `json:"sourceUrl,omitempty"`
	FetchedAt *time.Time            `json:"fetchedAt,omitempty"`
	Error     string                `json:"error,omitempty"`
	Sky       *starfront.SkySummary `json:"sky,omitempty"`
	Open      []Collab              `json:"open"`
	Closed    []Collab              `json:"closed"`
	ClosedAll int                   `json:"closedTotal"`
	Rig       rigsource.Rig         `json:"rig"`
	skybright.Value
	Night          *NightInfo `json:"night,omitempty"`
	SiteError      string     `json:"siteError,omitempty"`
	BestMonthHours float64    `json:"bestMonthHours"`
	MonthSample    string     `json:"monthSample"`
	Overlap        float64    `json:"overlap"`
}

const (
	ruleAny          = "any"
	criterionFilters = "Filters"
)

func CanonicalFilter(name string) string { return rigsource.CanonicalFilter(name) }

func filterOrder() []string { return rigsource.FilterOrder() }

func rigFilters(rig Rig) []string {
	var out []string
	for _, f := range filterOrder() {
		if _, ok := rig.Filters[f]; ok {
			out = append(out, f)
		}
	}
	return out
}

func span(lo, hi *float64, unit string) string {
	switch {
	case lo != nil && hi != nil:
		return fmt.Sprintf("%g–%g%s", *lo, *hi, unit)
	case lo != nil:
		return fmt.Sprintf("at least %g%s", *lo, unit)
	case hi != nil:
		return fmt.Sprintf("at most %g%s", *hi, unit)
	}
	return ruleAny
}

func within(v float64, lo, hi *float64) bool {
	return (lo == nil || v >= *lo) && (hi == nil || v <= *hi)
}

type evidence struct {
	night    *sky.Night
	region   starfront.Region
	practice practice
}

func criteria(p starfront.Project, rig Rig, info rigsource.Rig, ev evidence) []Criterion {
	r := p.Payload.Requirements
	fl, scale := rig.Frame.FocalLength, rig.Frame.Scale()
	var out []Criterion
	add := func(name, rule, you, result string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: result, Scope: ScopeRig})
	}
	tonight := func(name, rule, you, result string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: result, Scope: ScopeTonight})
	}
	limited := func(name string, v float64, lo, hi *float64, unit, you string) {
		switch {
		case lo == nil && hi == nil:
			add(name, ruleAny, you, CriterionNone)
		case !rig.known():
			add(name, span(lo, hi, unit), "not measured", CriterionOpen)
		case within(v, lo, hi):
			add(name, span(lo, hi, unit), you, CriterionPass)
		default:
			add(name, span(lo, hi, unit), you, CriterionFail)
		}
	}
	limited("Focal length", fl, r.MinFocalLength, r.MaxFocalLength, " mm", fmt.Sprintf("%g mm", fl))
	limited("Image scale", scale, r.MinScale, r.MaxScale, "″/px", fmt.Sprintf("%.3f″/px", scale))
	camera := "mono"
	if rig.Colour {
		camera = "colour"
	}
	switch {
	case info.Colour == nil:
		add("Camera", "mono or colour", "not measured", CriterionOpen)
	case r.AcceptColour == nil || *r.AcceptColour:
		add("Camera", "mono or colour", camera, CriterionNone)
	case rig.Colour:
		add("Camera", "mono only", camera, CriterionFail)
	default:
		add("Camera", "mono only", camera, CriterionPass)
	}
	if r.ColourMaxMoon != nil {
		rule := fmt.Sprintf("colour cameras only below %g%% Moon", *r.ColourMaxMoon*100)
		switch {
		case info.Colour == nil:
			add("Colour and Moon", rule, "camera type not measured", CriterionOpen)
		case !rig.Colour:
			add("Colour and Moon", rule, "mono: does not apply", CriterionPass)
		case ev.night == nil:
			tonight("Colour and Moon", rule, "tonight not computed", CriterionOpen)
		default:
			illum := ev.night.MoonIllumination()
			tonight("Colour and Moon", rule, fmt.Sprintf("tonight %.0f%% lit", illum*100), passIf(illum < *r.ColourMaxMoon))
		}
	}
	out = append(out, filterCriterion(r, rig, info))
	out = append(out, qualityCriteria(r, rig)...)
	if r.MinExposure != nil || r.MaxExposure != nil {
		var ok, bad []string
		for _, f := range filterOrder() {
			if e, has := rig.Exposures[f]; has && e > 0 {
				if within(e, r.MinExposure, r.MaxExposure) {
					ok = append(ok, fmt.Sprintf("%s %gs", f, e))
				} else {
					bad = append(bad, fmt.Sprintf("%s %gs", f, e))
				}
			}
		}
		switch {
		case len(ok) == 0 && len(bad) == 0:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), "not measured", CriterionOpen)
		case len(ok) == 0:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), strings.Join(bad, ", "), CriterionFail)
		default:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), strings.Join(ok, ", "), CriterionPass)
		}
	}
	if c, ok := moonCriterion(r, ev); ok {
		out = append(out, c)
	}
	if r.MinAltitude != nil {
		rule := fmt.Sprintf("at least %g°", *r.MinAltitude)
		if ev.night == nil {
			tonight("Altitude", rule, "tonight not computed", CriterionOpen)
		} else {
			w := ev.night.Window(ev.region.RA, ev.region.Dec, *r.MinAltitude)
			switch {
			case w.PeakAlt == nil:
				tonight("Altitude", rule, "no astronomical darkness tonight", CriterionOpen)
			case w.Hours > 0:
				tonight("Altitude", rule, fmt.Sprintf("tonight %s above %g° in the dark at the centre, peak %.0f°", hoursText(w.Hours), *r.MinAltitude, *w.PeakAlt), CriterionPass)
			default:
				tonight("Altitude", rule, fmt.Sprintf("tonight the centre does not reach %g° in the dark", *r.MinAltitude), CriterionFail)
			}
		}
	}
	if r.RequireCalibrated {
		out = append(out, ev.practice.calibrationCriterion())
	}
	if r.MinFramesPerVisit != nil {
		out = append(out, ev.practice.visitCriterion(*r.MinFramesPerVisit))
	}
	return out
}

func passIf(ok bool) string {
	if ok {
		return CriterionPass
	}
	return CriterionFail
}

func hoursText(h float64) string {
	if h < 1 {
		return fmt.Sprintf("%.0f min", h*60)
	}
	return fmt.Sprintf("%.1f h", h)
}

func moonCriterion(r starfront.Requirements, ev evidence) (Criterion, bool) {
	var rule []string
	if r.MaxMoonIllumination != nil {
		rule = append(rule, fmt.Sprintf("illumination at most %g%%", *r.MaxMoonIllumination*100))
	}
	if r.MinMoonSeparation != nil {
		rule = append(rule, fmt.Sprintf("at least %g° away", *r.MinMoonSeparation))
	}
	if len(rule) == 0 {
		return Criterion{}, false
	}
	c := Criterion{Name: "Moon", Rule: strings.Join(rule, ", "), Scope: ScopeTonight}
	if ev.night == nil {
		c.You, c.Result = "tonight not computed", CriterionOpen
		return c, true
	}
	sep := ev.night.MoonSeparation(ev.region.RA, ev.region.Dec)
	if sep == nil {
		c.You, c.Result = "no astronomical darkness tonight", CriterionOpen
		return c, true
	}
	illum := ev.night.MoonIllumination()
	var you []string
	ok := true
	if r.MaxMoonIllumination != nil {
		you = append(you, fmt.Sprintf("%.0f%% lit", illum*100))
		ok = ok && illum <= *r.MaxMoonIllumination
	}
	if r.MinMoonSeparation != nil {
		you = append(you, fmt.Sprintf("%.0f° from the centre at mid-darkness", *sep))
		ok = ok && *sep >= *r.MinMoonSeparation
	}
	c.You, c.Result = "tonight "+strings.Join(you, ", "), passIf(ok)
	return c, true
}

func qualityCriteria(r starfront.Requirements, rig Rig) []Criterion {
	var out []Criterion
	add := func(name, rule, you, result string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: result, Scope: ScopeRig})
	}
	if r.MaxHFR != nil {
		switch {
		case rig.TypicalHFR <= 0:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), "not measured", CriterionOpen)
		case rig.TypicalHFR <= *r.MaxHFR:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), fmt.Sprintf("%.1f″ typical", rig.TypicalHFR), CriterionPass)
		default:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), fmt.Sprintf("%.1f″ typical", rig.TypicalHFR), CriterionFail)
		}
	}
	if r.MaxGuideRMS != nil {
		switch {
		case rig.TypicalRMS <= 0:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), "not measured", CriterionOpen)
		case rig.TypicalRMS <= *r.MaxGuideRMS:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), fmt.Sprintf("%.2f″ typical", rig.TypicalRMS), CriterionPass)
		default:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), fmt.Sprintf("%.2f″ typical", rig.TypicalRMS), CriterionFail)
		}
	}
	return out
}

func canonicalHours(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[CanonicalFilter(k)] += v
	}
	return out
}

func wantedFilters(r starfront.Requirements) ([]string, map[string]*float64) {
	limits := map[string]*float64{}
	for k, v := range r.Filters {
		c := CanonicalFilter(k)
		if old, ok := limits[c]; ok && old != nil && (v == nil || *v > *old) {
			continue
		}
		limits[c] = v
	}
	var want []string
	for _, f := range filterOrder() {
		if _, ok := limits[f]; ok {
			want = append(want, f)
		}
	}
	var extra []string
	for f := range limits {
		if !slices.Contains(want, f) {
			extra = append(extra, f)
		}
	}
	slices.Sort(extra)
	return append(want, extra...), limits
}

func filterCriterion(r starfront.Requirements, rig Rig, info rigsource.Rig) Criterion {
	c := Criterion{Name: criterionFilters, Scope: ScopeRig}
	if len(r.Filters) == 0 {
		c.Rule, c.You, c.Result = ruleAny, strings.Join(rigFilters(rig), " "), CriterionNone
		return c
	}
	want, limits := wantedFilters(r)
	anyLimit := slices.ContainsFunc(want, func(f string) bool { return limits[f] != nil })
	c.Rule = strings.Join(want, " ")
	if !anyLimit {
		c.Rule += ", any bandpass"
	}
	var have, notes, unknown []string
	for _, f := range want {
		limit := limits[f]
		if f == colourFilter {
			switch {
			case info.Colour == nil:
				unknown = append(unknown, "OSC needs a colour camera; camera type not measured")
			case rig.Colour:
				have = append(have, f)
			default:
				notes = append(notes, "OSC needs a colour camera, yours is mono")
			}
			continue
		}
		if !slices.Contains(filterOrder(), f) {
			unknown = append(unknown, f+" is not a filter this rig records")
			continue
		}
		bp, mine := rig.Filters[f]
		switch {
		case !mine:
		case limit != nil && bp <= 0:
			unknown = append(unknown, fmt.Sprintf("%s bandpass not set (discover.filters), needs at most %g nm", f, *limit))
		case limit != nil && bp > *limit:
			notes = append(notes, fmt.Sprintf("%s is %g nm, over %g", f, bp, *limit))
		default:
			have = append(have, f)
		}
	}
	switch {
	case len(have) == len(want):
		c.You = fmt.Sprintf("all %d", len(want))
	case len(have) > 0:
		c.You = strings.Join(have, " ")
	case len(rig.Filters) == 0 && info.Colour != nil && !rig.Colour:
		c.You = "no filters measured"
	default:
		c.You = "none"
	}
	if len(notes)+len(unknown) > 0 {
		c.You += "; " + strings.Join(slices.Concat(notes, unknown), "; ")
	}
	switch {
	case len(have) > 0:
		c.Result = CriterionPass
	case len(unknown) > 0:
		c.Result = CriterionOpen
	default:
		c.Result = CriterionFail
	}
	return c
}

func verdict(cs []Criterion) (string, int) {
	fail, open := false, 0
	for _, c := range cs {
		if c.Scope != ScopeRig {
			continue
		}
		switch c.Result {
		case CriterionFail:
			fail = true
		case CriterionOpen:
			open++
		}
	}
	switch {
	case fail:
		return VerdictNoFit, open
	case open > 0:
		return VerdictUnchecked, open
	}
	return VerdictFits, 0
}

func regionHits(reg starfront.Region, subj Subject) bool {
	if reg.Width <= 0 || reg.Height <= 0 {
		return catalog.Separation(reg.RA, reg.Dec, subj.RA, subj.Dec) <= subj.Radius
	}
	if catalog.Separation(reg.RA, reg.Dec, subj.RA, subj.Dec) >= 60 {
		return false
	}
	xi, eta := mosaics.Project(mosaics.Point{RA: reg.RA, Dec: reg.Dec}, mosaics.Point{RA: subj.RA, Dec: subj.Dec})
	x, y := -xi, -eta
	rot := reg.Rotation * math.Pi / 180
	u := x*math.Cos(rot) - y*math.Sin(rot)
	v := x*math.Sin(rot) + y*math.Cos(rot)
	dx := math.Max(0, math.Abs(u)-reg.Width/2)
	dy := math.Max(0, math.Abs(v)-reg.Height/2)
	return math.Hypot(dx, dy) <= subj.Radius
}

func (s *Service) collab(ctx context.Context, p starfront.Project, st starfront.State, n *sky.Night, yr *sky.Year, subjects []Subject, rig Rig, info rigsource.Rig, pr practice) Collab {
	reg := p.Payload.Region
	c := Collab{
		ID: p.ID, Name: p.Name, Coordinator: p.Coordinator, Status: p.Status, Kind: p.Payload.Kind, Created: p.CreatedAt(),
		Notes: p.Payload.Notes, Region: reg, Goals: canonicalHours(p.Payload.Goals), Progress: map[string]float64{}, Have: []Have{},
		BestMonths: []int{},
	}
	if sum, ok := st.Summaries[p.ID]; ok {
		sum := sum
		c.Summary = &sum
		for f, h := range canonicalHours(sum.Hours) {
			c.Progress[f] = math.Round(h*100) / 100
		}
	}
	c.Criteria = criteria(p, rig, info, evidence{night: n, region: reg, practice: pr})
	c.Verdict, c.Unchecked = verdict(c.Criteria)
	c.Fits = c.Verdict == VerdictFits
	if fit := rig.fit(reg.Width*60, reg.Height*60); fit != nil {
		c.Panels, c.Columns, c.Rows = fit.Panels, fit.Columns, fit.Rows
		if c.Kind != "mosaic" {
			c.Panels, c.Columns, c.Rows = 1, 1, 1
		}
		if cov, ok := rig.Frame.Coverage(reg.Width, reg.Height); ok {
			cov = math.Round(cov*1000) / 1000
			c.Coverage = &cov
		}
	}
	if near, _ := s.Catalog.Cone(ctx, reg.RA, reg.Dec, math.Max(0.5, math.Min(reg.Width, reg.Height)/4)); len(near) > 0 {
		best := near[0]
		for _, o := range near {
			if len(o.Lists) > 0 && (len(best.Lists) == 0 || o.MajorArcmin > best.MajorArcmin) {
				best = o
			}
		}
		c.Near = best.Designation
		if best.Name != "" {
			c.Near += " " + best.Name
		}
	}
	if n != nil {
		obj := catalog.Object{RA: reg.RA, Dec: reg.Dec}
		c.Tonight = s.tonightFor(n, obj)
		if site, err := s.site(ctx); err == nil && n.Dusk != nil && n.Dawn != nil {
			c.Curve = site.Curve(reg.RA, reg.Dec, n.Dusk.Add(-time.Hour), n.Dawn.Add(time.Hour), 15*time.Minute)
		}
	}
	if yr != nil {
		c.Months = roundMonths(yr.Hours(reg.RA, reg.Dec, s.minAlt()))
		if best := bestMonths(c.Months); best != nil {
			c.BestMonths = best
		}
	}
	for _, subj := range subjects {
		if !subj.HasPos || subj.TotalHours() == 0 || !regionHits(reg, subj) {
			continue
		}
		h := Have{Subject: subj.Key, Name: subj.Name, ProjectID: subj.ProjectID, Hours: map[string]float64{}, LastNight: subj.LastNight, Subs: subj.Subs}
		for f, v := range subj.Hours {
			h.Hours[CanonicalFilter(f)] += math.Round(v*100) / 100
		}
		c.Have = append(c.Have, h)
	}
	return c
}

type sourced interface {
	URL() string
}

func (s *Service) Collabs(ctx context.Context, src CollabSource) (CollabsView, error) {
	rig, info := s.rig(ctx)
	v := CollabsView{Open: []Collab{}, Closed: []Collab{}, Rig: info, Value: s.skyValue(ctx), BestMonthHours: goodMonthHours, MonthSample: monthSampleText, Overlap: sky.DefaultOverlap}
	if src == nil {
		return v, nil
	}
	if u, ok := src.(sourced); ok {
		v.SourceURL = u.URL()
	}
	st := src.State()
	v.Enabled, v.FetchedAt, v.Error, v.Sky = st.Enabled, st.FetchedAt, st.Error, st.Sky
	n, siteErr := s.tonightNight(ctx)
	v.SiteError = siteErr
	if n != nil {
		v.Night = nightInfo(*n, s.minAlt(), s.minAltSource())
	}
	yr, _ := s.year(ctx, s.now().Year())
	subjects, err := s.Subjects(ctx)
	if err != nil {
		return v, err
	}
	pr := s.practice(ctx)
	cutoff := s.now().Add(-30 * 24 * time.Hour)
	for _, p := range st.Projects {
		if p.Status == starfront.StatusOpen {
			v.Open = append(v.Open, s.collab(ctx, p, st, n, yr, subjects, rig, info, pr))
			continue
		}
		v.ClosedAll++
		if p.CreatedAt().After(cutoff) {
			v.Closed = append(v.Closed, s.collab(ctx, p, st, n, yr, subjects, rig, info, pr))
		}
	}
	return v, nil
}

const practiceWindow = 60 * 24 * time.Hour

type practice struct {
	err        string
	visits     []int
	lights     int
	calibrated int
}

func (s *Service) practice(ctx context.Context) practice {
	var pr practice
	if s.AppDB == nil {
		pr.err = "the stacker database is not connected"
		return pr
	}
	since := s.now().Add(-practiceWindow)
	db := s.AppDB.WithContext(ctx)
	if !db.Migrator().HasTable(&app.Frame{}) {
		pr.err = "no lights indexed yet"
		return pr
	}
	if err := db.Model(&app.Frame{}).Select("COUNT(*)").
		Where("type = ? AND index_error IS NULL AND date_obs >= ? AND ts_target IS NOT NULL AND night IS NOT NULL", "LIGHT", since).
		Group("ts_target, night").Pluck("COUNT(*)", &pr.visits).Error; err != nil {
		pr.err = err.Error()
		return pr
	}
	if db.Migrator().HasTable(&app.StackFrame{}) {
		var row struct {
			Lights     int
			Calibrated int
		}
		if err := db.Model(&app.StackFrame{}).
			Select("COUNT(*) AS lights, COALESCE(SUM(CASE WHEN stack_frames.dark_master IS NOT NULL AND stack_frames.flat_master IS NOT NULL THEN 1 ELSE 0 END), 0) AS calibrated").
			Joins("JOIN frames ON frames.id = stack_frames.frame_id").
			Where("frames.type = ? AND frames.date_obs >= ?", "LIGHT", since).Scan(&row).Error; err != nil {
			pr.err = err.Error()
			return pr
		}
		pr.lights, pr.calibrated = row.Lights, row.Calibrated
	}
	return pr
}

func (pr practice) visitCriterion(minFrames int) Criterion {
	c := Criterion{Name: "Frames per visit", Rule: fmt.Sprintf("at least %d", minFrames), Scope: ScopeRig}
	switch {
	case pr.err != "":
		c.You, c.Result = "not checked: "+pr.err, CriterionOpen
	case len(pr.visits) == 0:
		c.You, c.Result = "not measured: no Target Scheduler lights in the last 60 days", CriterionOpen
	default:
		v := make([]float64, len(pr.visits))
		for i, n := range pr.visits {
			v[i] = float64(n)
		}
		med := medianOf(v)
		c.You = fmt.Sprintf("median %g lights per target per night over %d target-nights, last 60 days", med, len(v))
		c.Result = passIf(med >= float64(minFrames))
	}
	return c
}

func (pr practice) calibrationCriterion() Criterion {
	c := Criterion{Name: "Calibration", Rule: "calibrated frames", Scope: ScopeRig}
	switch {
	case pr.err != "":
		c.You, c.Result = "not checked: "+pr.err, CriterionOpen
	case pr.lights == 0:
		c.You, c.Result = "not measured: no lights processed by the stacker in the last 60 days", CriterionOpen
	default:
		c.You = fmt.Sprintf("%d of %d lights from the last 60 days calibrated with a dark and a flat", pr.calibrated, pr.lights)
		c.Result = passIf(pr.calibrated > 0)
	}
	return c
}

func medianOf(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}
