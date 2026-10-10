package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/starfront"
)

const (
	CriterionPass = "pass"
	CriterionFail = "fail"
	CriterionOpen = "open"
)

type CollabSource interface {
	State() starfront.State
}

type Criterion struct {
	Name   string `json:"name"`
	Rule   string `json:"rule"`
	You    string `json:"you"`
	Result string `json:"result"`
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
	Criteria    []Criterion         `json:"criteria"`
	Panels      int                 `json:"panels"`
	Columns     int                 `json:"columns"`
	Rows        int                 `json:"rows"`
	Coverage    float64             `json:"coverage"`
	Tonight     *Tonight            `json:"tonight,omitempty"`
	Curve       []sky.AltitudePoint `json:"curve,omitempty"`
	Months      [12]float64         `json:"months"`
	Have        []Have              `json:"have"`
}

type CollabsView struct {
	Enabled   bool                  `json:"enabled"`
	FetchedAt *time.Time            `json:"fetchedAt,omitempty"`
	Error     string                `json:"error,omitempty"`
	Sky       *starfront.SkySummary `json:"sky,omitempty"`
	Open      []Collab              `json:"open"`
	Closed    []Collab              `json:"closed"`
	ClosedAll int                   `json:"closedTotal"`
	Rig       RigInfo               `json:"rig"`
	Night     *NightInfo            `json:"night,omitempty"`
	SiteError string                `json:"siteError,omitempty"`
}

type RigInfo struct {
	FocalLength float64  `json:"focalLength"`
	Scale       float64  `json:"scale"`
	WidthDeg    float64  `json:"widthDeg"`
	HeightDeg   float64  `json:"heightDeg"`
	Colour      bool     `json:"colour"`
	Filters     []string `json:"filters"`
}

func CanonicalFilter(name string) string {
	n := strings.ToUpper(strings.TrimSpace(name))
	n = strings.NewReplacer("-", "", " ", "", "_", "").Replace(n)
	switch {
	case strings.HasPrefix(n, "HA") || strings.HasPrefix(n, "HALPHA") || n == "H":
		return "H"
	case strings.HasPrefix(n, "OIII") || n == "O3" || n == "O":
		return "O"
	case strings.HasPrefix(n, "SII") || n == "S2" || n == "S":
		return "S"
	case strings.HasPrefix(n, "L"):
		return "L"
	case strings.HasPrefix(n, "R"):
		return "R"
	case strings.HasPrefix(n, "G"):
		return "G"
	case strings.HasPrefix(n, "B"):
		return "B"
	}
	return n
}

func filterOrder() []string { return []string{"L", "R", "G", "B", "H", "O", "S"} }

func (s *Service) rigFilters() []string {
	var out []string
	for _, f := range filterOrder() {
		if _, ok := s.Rig.Filters[f]; ok {
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
	return "any"
}

func within(v float64, lo, hi *float64) bool {
	return (lo == nil || v >= *lo) && (hi == nil || v <= *hi)
}

func (s *Service) criteria(p starfront.Project) []Criterion {
	r := p.Payload.Requirements
	fl, scale := s.Rig.Frame.FocalLength, s.Rig.Frame.Scale()
	var out []Criterion
	add := func(name, rule, you, result string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: result})
	}
	limited := func(name string, v float64, lo, hi *float64, unit, you string) {
		switch {
		case lo == nil && hi == nil:
			add(name, "any", you, CriterionOpen)
		case within(v, lo, hi):
			add(name, span(lo, hi, unit), you, CriterionPass)
		default:
			add(name, span(lo, hi, unit), you, CriterionFail)
		}
	}
	limited("Focal length", fl, r.MinFocalLength, r.MaxFocalLength, " mm", fmt.Sprintf("%g mm", fl))
	limited("Image scale", scale, r.MinScale, r.MaxScale, "″/px", fmt.Sprintf("%.3f″/px", scale))
	camera := "mono"
	if s.Rig.Colour {
		camera = "colour"
	}
	switch {
	case r.AcceptColour == nil || *r.AcceptColour:
		add("Camera", "mono or colour", camera, CriterionPass)
	case s.Rig.Colour:
		add("Camera", "mono only", camera, CriterionFail)
	default:
		add("Camera", "mono only", camera, CriterionPass)
	}
	if r.ColourMaxMoon != nil {
		if s.Rig.Colour {
			add("Colour and Moon", fmt.Sprintf("colour cameras only below %g%% Moon", *r.ColourMaxMoon*100), "a rule you can meet", CriterionOpen)
		} else {
			add("Colour and Moon", fmt.Sprintf("colour cameras only below %g%% Moon", *r.ColourMaxMoon*100), "mono: does not apply", CriterionOpen)
		}
	}
	out = append(out, s.filterCriterion(r))
	out = append(out, s.qualityCriteria(r)...)
	if r.MinExposure != nil || r.MaxExposure != nil {
		var ok, bad []string
		for _, f := range filterOrder() {
			if e, has := s.Rig.Exposures[f]; has && e > 0 {
				if within(e, r.MinExposure, r.MaxExposure) {
					ok = append(ok, fmt.Sprintf("%s %gs", f, e))
				} else {
					bad = append(bad, fmt.Sprintf("%s %gs", f, e))
				}
			}
		}
		switch {
		case len(ok) == 0 && len(bad) == 0:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), "not set", CriterionOpen)
		case len(ok) == 0:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), strings.Join(bad, ", "), CriterionFail)
		default:
			add("Sub length", span(r.MinExposure, r.MaxExposure, " s"), strings.Join(ok, ", "), CriterionPass)
		}
	}
	moon := []string{}
	if r.MaxMoonIllumination != nil {
		moon = append(moon, fmt.Sprintf("illumination at most %g%%", *r.MaxMoonIllumination*100))
	}
	if r.MinMoonSeparation != nil {
		moon = append(moon, fmt.Sprintf("at least %g° away", *r.MinMoonSeparation))
	}
	if len(moon) > 0 {
		add("Moon", strings.Join(moon, ", "), "a rule your scheduler can meet", CriterionOpen)
	}
	if r.MinAltitude != nil {
		add("Altitude", fmt.Sprintf("at least %g°", *r.MinAltitude), "checked per cell", CriterionOpen)
	}
	if r.RequireCalibrated {
		add("Calibration", "calibrated frames", "the stacker calibrates every sub", CriterionOpen)
	}
	if r.MinFramesPerVisit != nil {
		add("Frames per visit", fmt.Sprintf("at least %d", *r.MinFramesPerVisit), "dealt per night", CriterionOpen)
	}
	return out
}

func (s *Service) qualityCriteria(r starfront.Requirements) []Criterion {
	var out []Criterion
	add := func(name, rule, you, result string) {
		out = append(out, Criterion{Name: name, Rule: rule, You: you, Result: result})
	}
	if r.MaxHFR != nil {
		switch {
		case s.Rig.TypicalHFR <= 0:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), "not measured", CriterionOpen)
		case s.Rig.TypicalHFR <= *r.MaxHFR:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), fmt.Sprintf("%.1f″ typical", s.Rig.TypicalHFR), CriterionPass)
		default:
			add("Star size", fmt.Sprintf("HFR at most %g″", *r.MaxHFR), fmt.Sprintf("%.1f″ typical", s.Rig.TypicalHFR), CriterionFail)
		}
	}
	if r.MaxGuideRMS != nil {
		switch {
		case s.Rig.TypicalRMS <= 0:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), "not measured", CriterionOpen)
		case s.Rig.TypicalRMS <= *r.MaxGuideRMS:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), fmt.Sprintf("%.2f″ typical", s.Rig.TypicalRMS), CriterionPass)
		default:
			add("Guiding", fmt.Sprintf("RMS at most %g″", *r.MaxGuideRMS), fmt.Sprintf("%.2f″ typical", s.Rig.TypicalRMS), CriterionFail)
		}
	}
	return out
}

func (s *Service) filterCriterion(r starfront.Requirements) Criterion {
	if len(r.Filters) == 0 {
		return Criterion{Name: "Filters", Rule: "any", You: strings.Join(s.rigFilters(), " "), Result: CriterionOpen}
	}
	var want, have, narrow []string
	anyLimit := false
	for _, f := range filterOrder() {
		limit, ok := r.Filters[f]
		if !ok {
			continue
		}
		want = append(want, f)
		if limit != nil {
			anyLimit = true
		}
		bp, mine := s.Rig.Filters[f]
		if !mine {
			continue
		}
		if limit != nil && bp > 0 && bp > *limit {
			narrow = append(narrow, fmt.Sprintf("%s is %g nm, over %g", f, bp, *limit))
			continue
		}
		have = append(have, f)
	}
	rule := strings.Join(want, " ")
	if !anyLimit {
		rule += ", any bandpass"
	}
	you := strings.Join(have, " ")
	switch {
	case len(have) == len(want):
		you = "all " + fmt.Sprint(len(want))
	case len(have) == 0:
		you = "none"
	}
	if len(narrow) > 0 {
		you += "; " + strings.Join(narrow, "; ")
	}
	result := CriterionPass
	if len(have) == 0 {
		result = CriterionFail
	}
	return Criterion{Name: "Filters", Rule: rule, You: you, Result: result}
}

func (s *Service) collab(ctx context.Context, p starfront.Project, st starfront.State, n *sky.Night, yr *sky.Year, subjects []Subject) Collab {
	reg := p.Payload.Region
	c := Collab{
		ID: p.ID, Name: p.Name, Coordinator: p.Coordinator, Status: p.Status, Kind: p.Payload.Kind, Created: p.CreatedAt(),
		Notes: p.Payload.Notes, Region: reg, Goals: p.Payload.Goals, Progress: map[string]float64{}, Have: []Have{},
	}
	if c.Goals == nil {
		c.Goals = map[string]float64{}
	}
	if sum, ok := st.Summaries[p.ID]; ok {
		sum := sum
		c.Summary = &sum
		for f, h := range sum.Hours {
			c.Progress[f] = math.Round(h*100) / 100
		}
	}
	c.Criteria = s.criteria(p)
	c.Fits = !slices.ContainsFunc(c.Criteria, func(cr Criterion) bool { return cr.Result == CriterionFail })
	fit := s.Rig.Frame.Fit(reg.Width*60, reg.Height*60, sky.DefaultOverlap)
	c.Panels, c.Columns, c.Rows = fit.Panels, fit.Columns, fit.Rows
	if c.Kind != "mosaic" {
		c.Panels, c.Columns, c.Rows = 1, 1, 1
	}
	c.Coverage = math.Round(s.Rig.Frame.Coverage(reg.Width, reg.Height)*1000) / 1000
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
	}
	reach := math.Hypot(reg.Width, reg.Height) / 2
	for _, subj := range subjects {
		if !subj.HasPos || subj.TotalHours() == 0 {
			continue
		}
		if catalog.Separation(reg.RA, reg.Dec, subj.RA, subj.Dec) > reach+subj.Radius {
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

func (s *Service) Collabs(ctx context.Context, src CollabSource) (CollabsView, error) {
	v := CollabsView{
		Open: []Collab{}, Closed: []Collab{},
		Rig: RigInfo{FocalLength: s.Rig.Frame.FocalLength, Scale: s.Rig.Frame.Scale(), WidthDeg: s.Rig.Frame.WidthDeg(), HeightDeg: s.Rig.Frame.HeightDeg(),
			Colour: s.Rig.Colour, Filters: s.rigFilters()},
	}
	if src == nil {
		return v, nil
	}
	st := src.State()
	v.Enabled, v.FetchedAt, v.Error, v.Sky = st.Enabled, st.FetchedAt, st.Error, st.Sky
	n, siteErr := s.tonightNight(ctx)
	v.SiteError = siteErr
	if n != nil {
		v.Night = nightInfo(*n, s.minAlt())
	}
	yr, _ := s.year(ctx, s.now().Year())
	subjects, err := s.Subjects(ctx)
	if err != nil {
		return v, err
	}
	cutoff := s.now().Add(-30 * 24 * time.Hour)
	for _, p := range st.Projects {
		if p.Status == starfront.StatusOpen {
			v.Open = append(v.Open, s.collab(ctx, p, st, n, yr, subjects))
			continue
		}
		v.ClosedAll++
		if p.CreatedAt().After(cutoff) {
			v.Closed = append(v.Closed, s.collab(ctx, p, st, n, yr, subjects))
		}
	}
	return v, nil
}
