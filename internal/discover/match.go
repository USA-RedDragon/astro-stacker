package discover

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const (
	MethodDesignation = "designation"
	MethodName        = "name"
	MethodSimilar     = "similar"
	MethodCoordinates = "coordinates"
	MethodFootprint   = "footprint"

	StatusAuto      = "auto"
	StatusInFrame   = "in-frame"
	StatusSuggested = "suggested"
	StatusConfirmed = app.XrefConfirmed
	StatusRejected  = app.XrefRejected

	autoConfidence     = 0.95
	minSuggestion      = 0.4
	linkedSuggestion   = 0.75
	maxSuggestions     = 3
	halfFrameDiagonal  = 2.0
	nearRadius         = 0.25
	nameAgreeRadius    = 0.5
	minFootprintArcmin = 2.0
)

type Link struct {
	Subject     string         `json:"subject"`
	SubjectName string         `json:"subjectName"`
	Object      catalog.Object `json:"object"`
	Method      string         `json:"method"`
	Confidence  float64        `json:"confidence"`
	Why         string         `json:"why"`
	Separation  float64        `json:"separation"`
	Status      string         `json:"status"`
}

func (l Link) Linked() bool {
	return l.Status == StatusAuto || l.Status == StatusConfirmed || l.Status == StatusInFrame
}

var nameSplit = regexp.MustCompile(`(?i)\s*(?:&|\+|/|,|\band\b)\s*`)

func objectRadius(o catalog.Object) float64 {
	return o.MajorArcmin / 120
}

type candidate struct {
	obj        catalog.Object
	nameMethod string
	nameSim    float64
	coord      bool
	footprint  bool
	sep        float64
}

func (s *Service) nameCandidates(ctx context.Context, subj Subject, add func(catalog.Object, string, float64)) {
	names := []string{subj.Name}
	for _, t := range subj.Targets {
		if st := catalog.StripPanel(t); !slices.Contains(names, st) {
			names = append(names, st)
		}
	}
	for _, n := range slices.Clone(names) {
		for _, part := range nameSplit.Split(n, -1) {
			if part = strings.TrimSpace(part); part != "" && !slices.Contains(names, part) {
				names = append(names, part)
			}
		}
	}
	for _, n := range names {
		if o, ok := s.Catalog.Lookup(n); ok {
			add(o, MethodDesignation, 1)
			continue
		}
		core := catalog.CoreName(n)
		for _, m := range s.Catalog.Find(ctx, n, 5) {
			switch m.How {
			case MethodDesignation:
				add(m.Object, MethodDesignation, 1)
			case MethodName:
				add(m.Object, MethodName, 1)
			case MethodSimilar:
				best := 0.0
				for _, alias := range append([]string{m.Object.Name}, m.Object.Aliases...) {
					best = math.Max(best, catalog.Similarity(catalog.CoreName(alias), core))
				}
				if best >= 0.5 {
					add(m.Object, MethodSimilar, best)
				}
			}
		}
	}
}

func (s *Service) notCatalogue(subj Subject) (string, bool) {
	check := func(n string) (string, bool) {
		n = catalog.StripPanel(n)
		if _, ok := s.Catalog.Lookup(n); ok {
			return "", false
		}
		return catalog.NotCatalogue(n)
	}
	if r, c := check(subj.Name); r != "" || len(subj.Targets) == 0 {
		return r, c
	}
	reason, certain := "", true
	for _, t := range subj.Targets {
		r, c := check(t)
		if r == "" {
			return "", false
		}
		reason, certain = r, certain && c
	}
	return reason, certain
}

func (s *Service) matchSubject(ctx context.Context, subj Subject) ([]Link, string) {
	cands := map[string]*candidate{}
	var order []string
	get := func(o catalog.Object) *candidate {
		c, ok := cands[o.ID]
		if !ok {
			c = &candidate{obj: o, sep: -1}
			cands[o.ID] = c
			order = append(order, o.ID)
		}
		return c
	}
	reason, certain := s.notCatalogue(subj)
	if !certain {
		s.nameCandidates(ctx, subj, func(o catalog.Object, method string, sim float64) {
			c := get(o)
			rank := map[string]int{MethodDesignation: 3, MethodName: 2, MethodSimilar: 1, "": 0}
			if rank[method] > rank[c.nameMethod] || method == c.nameMethod && sim > c.nameSim {
				c.nameMethod, c.nameSim = method, sim
			}
		})
	}
	named := len(order) > 0
	footprint := subj.Radius + halfFrameDiagonal/2
	if subj.HasPos {
		near, _ := s.Catalog.Cone(ctx, subj.RA, subj.Dec, subj.Radius+halfFrameDiagonal)
		for _, o := range near {
			d := catalog.Separation(subj.RA, subj.Dec, o.RA, o.Dec)
			r := objectRadius(o)
			notable := len(o.Lists) > 0 || o.Name != ""
			centred := notable && d <= math.Max(r, nearRadius) && (!subj.Mosaic || o.MajorArcmin/60 >= subj.Radius)
			inside := len(o.Lists) > 0 && o.MajorArcmin >= minFootprintArcmin && d+r <= footprint
			if centred || inside {
				c := get(o)
				c.coord, c.footprint = centred, inside
			}
		}
		for _, c := range cands {
			c.sep = catalog.Separation(subj.RA, subj.Dec, c.obj.RA, c.obj.Dec)
		}
	}
	if reason != "" && !certain && (named || slices.ContainsFunc(order, func(id string) bool { return cands[id].coord })) {
		reason = ""
	}
	links := make([]Link, 0, len(order))
	for _, id := range order {
		c := cands[id]
		l := score(subj, c, footprint)
		if l.Confidence <= 0 || reason != "" && l.Status != StatusInFrame {
			continue
		}
		links = append(links, l)
	}
	slices.SortStableFunc(links, func(a, b Link) int {
		if a.Confidence != b.Confidence {
			if a.Confidence > b.Confidence {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Object.ID, b.Object.ID)
	})
	return links, reason
}

func score(subj Subject, c *candidate, footprint float64) Link {
	l := Link{Subject: subj.Key, SubjectName: subj.Name, Object: c.obj, Separation: math.Round(c.sep*1000) / 1000}
	r := objectRadius(c.obj)
	agrees := !subj.HasPos || c.sep >= 0 && c.sep <= math.Max(math.Max(r, nameAgreeRadius), footprint)
	where := ""
	if subj.HasPos && c.sep >= 0 {
		where = fmt.Sprintf("Centres %s apart.", angle(c.sep))
	}
	switch {
	case c.nameMethod == MethodDesignation && agrees && subj.HasPos:
		l.Method, l.Confidence, l.Why = MethodDesignation, 0.99, "Your name is its catalogue designation. "+where
	case c.nameMethod == MethodName && agrees && subj.HasPos:
		l.Method, l.Confidence, l.Why = MethodName, 0.96, "Your name is its common name. "+where
	case c.nameMethod == MethodDesignation && !subj.HasPos:
		l.Method, l.Confidence, l.Why = MethodDesignation, 0.9, "Your name is its catalogue designation; no coordinates to check."
	case c.nameMethod == MethodName && !subj.HasPos:
		l.Method, l.Confidence, l.Why = MethodName, 0.85, "Your name is its common name; no coordinates to check."
	case c.nameMethod == MethodSimilar && agrees:
		l.Method, l.Confidence = MethodSimilar, math.Min(0.92, 0.55+0.4*c.nameSim)
		l.Why = "Name is spelled differently. " + where
		if !subj.HasPos {
			l.Confidence = math.Min(l.Confidence, 0.6)
		}
	case c.nameMethod != "" && !agrees:
		l.Method, l.Confidence = c.nameMethod, 0.4
		l.Why = "Names match, but " + strings.ToLower(where[:1]) + where[1:]
	case c.coord:
		near := 1 - c.sep/math.Max(r, nearRadius)
		l.Method, l.Confidence = MethodCoordinates, 0.5+0.3*near
		if subj.Mosaic && c.obj.MajorArcmin/60 >= subj.Radius*1.5 {
			l.Confidence += 0.05
		}
		l.Why = "No name in common. " + where
	case c.footprint:
		l.Method, l.Confidence, l.Why = MethodFootprint, 0.5, "Inside your frame. "+where
	}
	l.Confidence = math.Round(l.Confidence*100) / 100
	l.Why = strings.TrimSpace(l.Why)
	switch {
	case l.Method == MethodFootprint:
		l.Status = StatusInFrame
	case l.Confidence >= autoConfidence:
		l.Status = StatusAuto
	default:
		l.Status = StatusSuggested
	}
	return l
}

func angle(deg float64) string {
	switch {
	case deg < 1.0/60:
		return fmt.Sprintf("%.0f″", deg*3600)
	case deg < 1:
		return fmt.Sprintf("%.0f′", deg*60)
	}
	return fmt.Sprintf("%.1f°", deg)
}

func applyDecisions(links []Link, decisions map[string]map[string]string) []Link {
	out := links[:0]
	for _, l := range links {
		if d, ok := decisions[l.Subject][l.Object.ID]; ok {
			l.Status = d
		}
		out = append(out, l)
	}
	return out
}

func trimSuggestions(links []Link) []Link {
	hasPrimary := slices.ContainsFunc(links, func(l Link) bool {
		return (l.Status == StatusAuto || l.Status == StatusConfirmed) && l.Method != MethodFootprint
	})
	out := make([]Link, 0, len(links))
	n := 0
	for _, l := range links {
		if l.Status == StatusSuggested {
			if l.Confidence < minSuggestion || hasPrimary && l.Confidence < linkedSuggestion || n >= maxSuggestions {
				continue
			}
			n++
		}
		out = append(out, l)
	}
	return out
}

func (s *Service) decisions(ctx context.Context) (map[string]map[string]string, []app.ObjectXref, error) {
	var rows []app.ObjectXref
	if err := s.AppDB.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, nil, fmt.Errorf("load name matches: %w", err)
	}
	out := map[string]map[string]string{}
	for _, r := range rows {
		if out[r.Subject] == nil {
			out[r.Subject] = map[string]string{}
		}
		out[r.Subject][r.ObjectID] = r.Decision
	}
	return out, rows, nil
}
