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

func narrowband(t string) string {
	switch t {
	case catalog.TypeEmission, catalog.TypeClusterNebula:
		return "H-α emission"
	case catalog.TypeSNR:
		return "H-α and O-III filaments"
	case catalog.TypePN:
		return "O-III and H-α"
	case catalog.TypeNebula:
		return "Possibly H-α"
	}
	return "Broadband"
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

type FinderRow struct {
	Object      catalog.Object `json:"object"`
	Group       string         `json:"group"`
	Fit         sky.Fit        `json:"fit"`
	Brightness  string         `json:"brightness"`
	BrightScore float64        `json:"brightScore"`
	Narrowband  string         `json:"narrowband"`
	Months      [12]float64    `json:"months"`
	BestMonths  []int          `json:"bestMonths"`
	Tonight     float64        `json:"tonightHours"`
	Score       float64        `json:"score"`
	Imaged      bool           `json:"imaged"`
	Subjects    []SubjectRef   `json:"subjects"`
	Rotation    float64        `json:"rotation"`
	CatalogGap  bool           `json:"catalogueGap"`
}

type FinderResult struct {
	Total     int         `json:"total"`
	Rows      []FinderRow `json:"rows"`
	SiteError string      `json:"siteError,omitempty"`
	Frame     FrameInfo   `json:"frame"`
	Sky       float64     `json:"skyBrightness"`
}

type FrameInfo struct {
	WidthDeg  float64 `json:"widthDeg"`
	HeightDeg float64 `json:"heightDeg"`
	Scale     float64 `json:"scale"`
}

type finderCache struct {
	day  time.Time
	rows []FinderRow
}

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

func (s *Service) brightness(o catalog.Object) (string, float64) {
	skyMag := s.Rig.SkyBright
	if skyMag == 0 {
		skyMag = 21.4
	}
	if o.BrightScore != nil {
		return o.Brightness, *o.BrightScore
	}
	sb := o.SurfaceBrightness
	if sb == nil && o.Magnitude != nil && o.MajorArcmin > 0 {
		minor := o.MinorArcmin
		if minor == 0 {
			minor = o.MajorArcmin
		}
		area := math.Pi / 4 * o.MajorArcmin * minor * 3600
		v := *o.Magnitude + 2.5*math.Log10(area)
		sb = &v
	}
	if sb != nil {
		margin := skyMag - *sb
		return fmt.Sprintf("%.1f mag/arcsec², %+.1f against your sky", *sb, margin), math.Max(0, math.Min(1, (margin+3.5)/4))
	}
	return "Brightness not catalogued", 0.5
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

func (s *Service) finderRows(ctx context.Context) ([]FinderRow, error) {
	site, err := s.site(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	day := site.LocalNoon(now)
	s.mu.Lock()
	cached := s.finder
	s.mu.Unlock()
	if cached != nil && cached.day.Equal(day) {
		return cached.rows, nil
	}
	yr, err := s.year(ctx, now.Year())
	if err != nil {
		return nil, err
	}
	n, err := s.night(ctx, now)
	if err != nil {
		return nil, err
	}
	minAlt := s.minAlt()
	maxDec := site.Latitude - 90 + minAlt
	var rows []FinderRow
	for _, o := range s.Catalog.All() {
		g := TypeGroup(o.Type)
		if g == "" || o.MajorArcmin <= 0 || o.Dec < maxDec {
			continue
		}
		fit := s.Rig.Frame.Fit(o.MajorArcmin, o.MinorArcmin, sky.DefaultOverlap)
		if fit.Fill < minFinderFill {
			continue
		}
		months := roundMonths(yr.Hours(o.RA, o.Dec, minAlt))
		best := bestMonths(months)
		if len(best) == 0 {
			continue
		}
		label, bright := s.brightness(o)
		maxH := slices.Max(months[:])
		score := 0.35*fillScore(fit) + 0.25*bright + 0.25*math.Min(1, maxH/8)
		gap := len(o.Lists) > 0
		if gap {
			score += 0.15
		}
		rows = append(rows, FinderRow{
			Object: o, Group: g, Fit: fit, Brightness: label, BrightScore: math.Round(bright*100) / 100, Narrowband: narrowband(o.Type),
			Months: months, BestMonths: best, Tonight: n.HoursAbove(o.RA, o.Dec, minAlt), Score: math.Round(score*1000) / 1000,
			Rotation: o.PA, CatalogGap: gap,
		})
	}
	s.mu.Lock()
	s.finder = &finderCache{day: day, rows: rows}
	s.mu.Unlock()
	return rows, nil
}

func (s *Service) Finder(ctx context.Context, q FinderQuery) (FinderResult, error) {
	out := FinderResult{
		Frame: FrameInfo{WidthDeg: s.Rig.Frame.WidthDeg(), HeightDeg: s.Rig.Frame.HeightDeg(), Scale: s.Rig.Frame.Scale()},
		Sky:   s.Rig.SkyBright, Rows: []FinderRow{},
	}
	rows, siteErr := s.finderRows(ctx)
	if siteErr != nil {
		out.SiteError = siteErr.Error()
	}
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
		if r.Imaged {
			r.Score = math.Round((r.Score-0.15*boolf(r.CatalogGap))*1000) / 1000
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

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
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
