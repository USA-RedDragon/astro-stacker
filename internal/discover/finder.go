package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skybright"
)

const (
	GroupEmission   = "em"
	GroupRemnant    = "snr"
	GroupDark       = "dark"
	GroupReflection = "ref"
	GroupPlanetary  = "pn"
	GroupGalaxy     = "gal"

	SortScore = "score"
	SortFill  = "fill"
	SortNow   = "now"

	goodMonthHours = 4.0
	minFinderFill  = 0.02
)

func TypeGroup(t string) string {
	switch t {
	case catalog.TypeEmission, catalog.TypeNebula, catalog.TypeClusterNebula:
		return GroupEmission
	case catalog.TypeSNR:
		return GroupRemnant
	case catalog.TypeDark:
		return GroupDark
	case catalog.TypeReflection:
		return GroupReflection
	case catalog.TypePN:
		return GroupPlanetary
	case catalog.TypeGalaxy, catalog.TypeGalaxyGroup:
		return GroupGalaxy
	}
	return ""
}

func halphaWeighted(group string) bool {
	switch group {
	case GroupEmission, GroupRemnant, GroupPlanetary:
		return true
	}
	return false
}

const (
	weightFill   = 0.35
	weightHours  = 0.25
	weightHAlpha = 0.15
	weightGap    = 0.15
	fullHours    = 8.0

	TermFill   = "fill"
	TermHours  = "hours"
	TermHAlpha = "halpha"
	TermGap    = "gap"
)

type ScoreWeight struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
	Rule   string  `json:"rule"`
}

func ScoreWeights() []ScoreWeight {
	return []ScoreWeight{
		{TermFill, "Frame fill", weightFill, "1 when the long side fills 30–90% of the frame's long side, 0.9 above 90%, fill ÷ 30% below; a mosaic of N panels gets 0.8 ÷ N^0.35"},
		{TermHours, "Dark hours", weightHours, fmt.Sprintf("hours above the minimum altitude in astronomical darkness in the best month ÷ %g h, at most 1", fullHours)},
		{TermHAlpha, "H-α", weightHAlpha, "emission nebulae, remnants and planetaries only: log10(R ÷ 2) ÷ log10(250) from the H-α map, 0 at 2 R or less, 1 at 500 R or more"},
		{TermGap, "Catalogue gap", weightGap, "on a tracked catalogue list and not imaged yet"},
	}
}

type ScoreTerm struct {
	Key    string   `json:"key"`
	Value  *float64 `json:"value"`
	Points float64  `json:"points"`
	Detail string   `json:"detail"`
}

type FinderQuery struct {
	Fits    []string
	Types   []string
	Months  []int
	MinFill float64
	Imaged  bool
	Sort    string
	Limit   int
}

type Brightness struct {
	Text   string   `json:"text"`
	Kind   string   `json:"kind"`
	Value  *float64 `json:"value"`
	Band   string   `json:"band,omitempty"`
	Source string   `json:"source,omitempty"`
}

const (
	BrightClass      = "class"
	BrightCatalogued = "catalogued"
	BrightComputed   = "computed"
	BrightNone       = "none"
)

type FinderRow struct {
	Object     catalog.Object `json:"object"`
	Group      string         `json:"group"`
	Fit        sky.Fit        `json:"fit"`
	Brightness Brightness     `json:"brightness"`
	HAlpha     *halpha.Sample `json:"halpha"`
	Months     [12]float64    `json:"months"`
	BestMonths []int          `json:"bestMonths"`
	Tonight    float64        `json:"tonightHours"`
	Score      float64        `json:"score"`
	Terms      []ScoreTerm    `json:"terms"`
	Imaged     bool           `json:"imaged"`
	Subjects   []SubjectRef   `json:"subjects"`
	Rotation   *float64       `json:"rotation"`
	CatalogGap bool           `json:"catalogueGap"`
}

type Exclusion struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type FinderResult struct {
	Total           int           `json:"total"`
	Rows            []FinderRow   `json:"rows"`
	SiteError       string        `json:"siteError,omitempty"`
	RigError        *string       `json:"rigError"`
	Frame           FrameInfo     `json:"frame"`
	Rig             rigsource.Rig `json:"rig"`
	HAlphaMap       halpha.Status `json:"halphaMap"`
	Weights         []ScoreWeight `json:"weights"`
	ScoreMax        float64       `json:"scoreMax"`
	MinAltitude     float64       `json:"minAltitude"`
	MinAltitudeFrom string        `json:"minAltitudeSource"`
	BestMonthHours  float64       `json:"bestMonthHours"`
	MonthSample     string        `json:"monthSample"`
	Overlap         float64       `json:"overlap"`
	MinFill         float64       `json:"minFill"`
	Excluded        []Exclusion   `json:"excluded"`
	skybright.Value
}

type FrameInfo struct {
	WidthDeg  float64 `json:"widthDeg"`
	HeightDeg float64 `json:"heightDeg"`
	Scale     float64 `json:"scale"`
}

type finderCache struct {
	day      time.Time
	key      string
	rows     []FinderRow
	excluded []Exclusion
}

const errRigUnknown = "the rig is not known yet: no lights with FOCALLEN, XPIXSZ and image size have been indexed"

func fillScore(f sky.Fit) float64 {
	switch {
	case f.Panels > 1:
		return 0.8 / math.Pow(float64(f.Panels), 0.35)
	case f.Fill >= 0.3 && f.Fill <= 0.9:
		return 1
	case f.Fill > 0.9:
		return 0.9
	}
	return math.Max(0, f.Fill/0.3)
}

func sbBand(o catalog.Object) (band, source string) {
	return "", o.Source
}

func brightness(o catalog.Object) Brightness {
	if o.Brightness != "" {
		return Brightness{Text: o.Brightness, Kind: BrightClass, Source: o.Source}
	}
	if o.SurfaceBrightness != nil {
		v := *o.SurfaceBrightness
		band, src := sbBand(o)
		return Brightness{Text: fmt.Sprintf("%.1f mag/arcsec² catalogued", v), Kind: BrightCatalogued, Value: &v, Band: band, Source: src}
	}
	if o.Magnitude != nil && o.MajorArcmin > 0 {
		minor := o.Minor()
		if minor <= 0 {
			minor = o.MajorArcmin
		}
		area := math.Pi / 4 * o.MajorArcmin * minor * 3600
		v := math.Round((*o.Magnitude+2.5*math.Log10(area))*10) / 10
		return Brightness{Text: fmt.Sprintf("≈ %.1f mag/arcsec² computed from magnitude %.1f spread over the catalogued size", v, *o.Magnitude), Kind: BrightComputed, Value: &v}
	}
	return Brightness{Text: "Brightness not catalogued", Kind: BrightNone}
}

func bestMonths(m [12]float64) []int {
	var out []int
	for i, h := range m {
		if h >= goodMonthHours {
			out = append(out, i+1)
		}
	}
	return out
}

func ptrf(v float64) *float64 { return &v }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func scoreTerms(o catalog.Object, g string, fit sky.Fit, maxH float64, ha *halpha.Sample, gap bool) []ScoreTerm {
	fs := fillScore(fit)
	fillDetail := fmt.Sprintf("fills %.0f%% of the frame's long side", fit.Fill*100)
	if fit.Panels > 1 {
		fillDetail = fmt.Sprintf("%d panels", fit.Panels)
	}
	hs := math.Min(1, maxH/fullHours)
	terms := []ScoreTerm{
		{Key: TermFill, Value: ptrf(round3(fs)), Points: round3(weightFill * fs), Detail: fillDetail},
		{Key: TermHours, Value: ptrf(round3(hs)), Points: round3(weightHours * hs), Detail: fmt.Sprintf("%.1f h in the best month", maxH)},
	}
	if halphaWeighted(g) {
		t := ScoreTerm{Key: TermHAlpha, Detail: "not on the H-α map"}
		if ha != nil {
			v := halpha.Score(ha)
			t.Value, t.Points, t.Detail = ptrf(round3(v)), round3(weightHAlpha*v), fmt.Sprintf("%.0f R mean within %.2f°", ha.Rayleigh, ha.RadiusDeg)
		}
		terms = append(terms, t)
	}
	if gap {
		terms = append(terms, ScoreTerm{Key: TermGap, Value: ptrf(1), Points: weightGap, Detail: "on " + strings.Join(o.Lists, ", ")})
	}
	return terms
}

func sumTerms(ts []ScoreTerm) float64 {
	var s float64
	for _, t := range ts {
		s += t.Points
	}
	return round3(s)
}

const (
	excludeType   = "not an emission, remnant, dark, reflection, planetary or galaxy type"
	excludeSize   = "size not catalogued"
	excludeLow    = "never clears the minimum altitude from your latitude"
	excludeSmall  = "fills less than 2% of the frame's long side"
	excludeMonths = "no month with enough dark hours above the minimum altitude"
)

func (s *Service) finderRows(ctx context.Context, rig Rig) ([]FinderRow, []Exclusion, error) {
	site, err := s.site(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !rig.known() {
		return nil, nil, nil
	}
	now := s.now()
	day := site.LocalNoon(now)
	hmap, hst := s.halphaMap()
	key := fmt.Sprintf("%+v|%s|%v", rig.Frame, hst.State, hst.FetchedAt)
	s.mu.Lock()
	cached := s.finder
	s.mu.Unlock()
	if cached != nil && cached.day.Equal(day) && cached.key == key {
		return cached.rows, cached.excluded, nil
	}
	yr, err := s.year(ctx, now.Year())
	if err != nil {
		return nil, nil, err
	}
	n, err := s.night(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	minAlt := s.minAlt()
	maxDec := site.Latitude - 90 + minAlt
	counts := map[string]int{}
	var rows []FinderRow
	for _, o := range s.Catalog.All() {
		g := TypeGroup(o.Type)
		switch {
		case g == "":
			counts[excludeType]++
			continue
		case o.MajorArcmin <= 0:
			counts[excludeSize]++
			continue
		case o.Dec < maxDec:
			counts[excludeLow]++
			continue
		}
		fit := rig.Frame.Fit(o.MajorArcmin, o.Minor(), sky.DefaultOverlap)
		if fit.Fill < minFinderFill {
			counts[excludeSmall]++
			continue
		}
		months := roundMonths(yr.Hours(o.RA, o.Dec, minAlt))
		best := bestMonths(months)
		if len(best) == 0 {
			counts[excludeMonths]++
			continue
		}
		var ha *halpha.Sample
		if smp, ok := hmap.Sample(o.RA, o.Dec, o.MajorArcmin/120); ok {
			ha = &smp
		}
		gap := len(o.Lists) > 0
		terms := scoreTerms(o, g, fit, slices.Max(months[:]), ha, gap)
		rows = append(rows, FinderRow{
			Object: o, Group: g, Fit: fit, Brightness: brightness(o), HAlpha: ha,
			Months: months, BestMonths: best, Tonight: n.HoursAbove(o.RA, o.Dec, minAlt), Score: sumTerms(terms), Terms: terms,
			Rotation: rotationFor(fit, o.PA), CatalogGap: gap,
		})
	}
	var excluded []Exclusion
	for _, r := range []string{excludeType, excludeSize, excludeLow, excludeSmall, excludeMonths} {
		if counts[r] > 0 {
			excluded = append(excluded, Exclusion{Reason: r, Count: counts[r]})
		}
	}
	s.mu.Lock()
	s.finder = &finderCache{day: day, key: key, rows: rows, excluded: excluded}
	s.mu.Unlock()
	return rows, excluded, nil
}

func (s *Service) Finder(ctx context.Context, q FinderQuery) (FinderResult, error) {
	rig, info := s.rig(ctx)
	_, hst := s.halphaMap()
	out := FinderResult{Value: s.skyValue(ctx), Rows: []FinderRow{}, Rig: info, HAlphaMap: hst, Weights: ScoreWeights(),
		ScoreMax: weightFill + weightHours + weightHAlpha + weightGap, MinAltitude: s.minAlt(), MinAltitudeFrom: MinAltitudeSetting,
		BestMonthHours: goodMonthHours, MonthSample: monthSampleText, Overlap: sky.DefaultOverlap, MinFill: minFinderFill, Excluded: []Exclusion{}}
	if rig.known() {
		out.Frame = FrameInfo{WidthDeg: rig.Frame.WidthDeg(), HeightDeg: rig.Frame.HeightDeg(), Scale: rig.Frame.Scale()}
	} else {
		msg := errRigUnknown
		out.RigError = &msg
	}
	rows, excluded, siteErr := s.finderRows(ctx, rig)
	if siteErr != nil {
		out.SiteError = siteErr.Error()
	}
	out.Excluded = append(out.Excluded, excluded...)
	snap, err := s.snapshot(ctx)
	if err != nil {
		return out, err
	}
	subjects := subjectMap(snap)
	var hits []FinderRow
	for _, r := range rows {
		if len(q.Fits) > 0 && !slices.Contains(q.Fits, r.Fit.Category) {
			continue
		}
		if len(q.Types) > 0 && !slices.Contains(q.Types, r.Group) {
			continue
		}
		if r.Fit.Fill < q.MinFill && r.Fit.Panels == 1 {
			continue
		}
		if len(q.Months) > 0 && !slices.ContainsFunc(q.Months, func(m int) bool { return slices.Contains(r.BestMonths, m) }) {
			continue
		}
		r.Subjects = []SubjectRef{}
		for _, l := range snap.byObject[r.Object.ID] {
			if !l.Linked() {
				continue
			}
			subj := subjects[l.Subject]
			r.Imaged = true
			r.Subjects = append(r.Subjects, SubjectRef{Key: subj.Key, Name: subj.Name, ProjectID: subj.ProjectID, State: subj.StateName, Status: l.Status, Method: l.Method, Done: subj.Done})
		}
		if r.Imaged && !q.Imaged {
			continue
		}
		if r.Imaged && r.CatalogGap {
			r.Terms = slices.DeleteFunc(slices.Clone(r.Terms), func(t ScoreTerm) bool { return t.Key == TermGap })
			r.Score = sumTerms(r.Terms)
		}
		hits = append(hits, r)
	}
	sortRows(hits, q.Sort)
	out.Total = len(hits)
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out.Rows = append(out.Rows, hits...)
	return out, nil
}

func sortRows(rows []FinderRow, by string) {
	key := func(r FinderRow) float64 {
		switch strings.ToLower(by) {
		case SortFill:
			return math.Min(r.Fit.Fill, 1)*10 - float64(r.Fit.Panels)
		case SortNow:
			return r.Tonight*10 + r.Score
		}
		return r.Score
	}
	slices.SortStableFunc(rows, func(a, b FinderRow) int {
		ka, kb := key(a), key(b)
		if ka != kb {
			if ka > kb {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Object.ID, b.Object.ID)
	})
}

func rotationFor(fit sky.Fit, pa *float64) *float64 {
	if pa == nil {
		return nil
	}
	r := fit.Rotation(*pa)
	return &r
}
