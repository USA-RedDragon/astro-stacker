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
	MethodFootprint   = "footprint"
	MethodManual      = "manual"

	StatusAuto      = "auto"
	StatusImaged    = "imaged"
	StatusPlanned   = "planned"
	StatusSuggested = "suggested"
	StatusConfirmed = app.XrefConfirmed
	StatusRejected  = app.XrefRejected

	BasisFrames   = "frames"
	BasisPointing = "pointing"
	BasisTarget   = "target"
	BasisPlan     = "plan"
	BasisName     = "name"
	BasisManual   = "manual"

	RuleInsideFrames  = "inside-frames"
	RuleInsidePlan    = "inside-planned-frame"
	RuleNameOutside   = "name-outside-frames"
	RuleNameAtPlace   = "exact-name-at-position"
	RuleNameFar       = "exact-name-far-from-position"
	RuleNameOnly      = "exact-name-no-position"
	RuleSimilarName   = "similar-name"
	RuleConfirmed     = "confirmed-by-you"
	minNameSimilarity = 0.5
)

type Link struct {
	Subject      string         `json:"subject"`
	SubjectName  string         `json:"subjectName"`
	Object       catalog.Object `json:"object"`
	Method       string         `json:"method"`
	Rule         string         `json:"rule"`
	Why          string         `json:"why"`
	Separation   *float64       `json:"separation"`
	AgreeRadius  *float64       `json:"agreeRadius,omitempty"`
	Similarity   *float64       `json:"similarity,omitempty"`
	Status       string         `json:"status"`
	Basis        string         `json:"basis"`
	Coverage     *float64       `json:"coverage"`
	CentreInside bool           `json:"centreInside"`
	Targets      []string       `json:"targets,omitempty"`
	NameMatch    string         `json:"nameMatch,omitempty"`
}

func (l Link) Linked() bool {
	switch l.Status {
	case StatusAuto, StatusConfirmed, StatusImaged, StatusPlanned:
		return true
	}
	return false
}

func round3p(v float64) *float64 {
	r := round3(v)
	return &r
}

var nameSplit = regexp.MustCompile(`(?i)\s*(?:&|\+|/|,|\band\b)\s*`)

func objectRadius(o catalog.Object) float64 {
	return o.MajorArcmin / 120
}

type nameAdd func(o catalog.Object, method string, sim float64, whole, part string)

func singular(word string) (string, bool) {
	switch strings.ToLower(word) {
	case "nebulae", "nebulas":
		return "Nebula", true
	case "galaxies":
		return "Galaxy", true
	case "clusters":
		return "Cluster", true
	}
	return "", false
}

func compositeParts(name string) []string {
	var out []string
	for _, p := range nameSplit.Split(name, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return nil
	}
	last := strings.Fields(out[len(out)-1])
	if one, ok := singular(last[len(last)-1]); ok {
		last[len(last)-1] = one
		out[len(out)-1] = strings.Join(last, " ")
		for i := range out[:len(out)-1] {
			if len(strings.Fields(out[i])) == 1 {
				out[i] += " " + one
			}
		}
	}
	return out
}

func andKey(name string) string {
	parts := strings.Split(" "+catalog.NormalizeName(name)+" ", " and ")
	for i := range parts {
		parts[i] = catalog.CoreName(parts[i])
	}
	return strings.Join(parts, "+")
}

func (s *Service) wholeName(ctx context.Context, name string) bool {
	if _, ok := s.Catalog.Lookup(name); ok {
		return true
	}
	key := andKey(name)
	for _, m := range s.Catalog.Find(ctx, name, 5) {
		if m.How == MethodDesignation {
			return true
		}
		if m.How != MethodName {
			continue
		}
		for _, alias := range append([]string{m.Object.Name}, m.Object.Aliases...) {
			if alias != "" && andKey(alias) == key {
				return true
			}
		}
	}
	return false
}

func (s *Service) nameCandidates(ctx context.Context, subj Subject, add nameAdd) {
	var names []string
	for _, n := range append([]string{subj.Name}, subj.Targets...) {
		if n = catalog.StripPanel(n); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	for _, n := range names {
		parts := compositeParts(n)
		if parts == nil || s.wholeName(ctx, n) {
			s.matchName(ctx, n, "", add)
			continue
		}
		for _, p := range parts {
			s.matchName(ctx, p, n, add)
		}
	}
}

func (s *Service) matchName(ctx context.Context, n, whole string, add nameAdd) {
	if o, ok := s.Catalog.Lookup(n); ok {
		add(o, MethodDesignation, 1, whole, n)
		return
	}
	core := catalog.CoreName(n)
	for _, m := range s.Catalog.Find(ctx, n, 5) {
		switch m.How {
		case MethodDesignation:
			add(m.Object, MethodDesignation, 1, whole, n)
		case MethodName:
			add(m.Object, MethodName, 1, whole, n)
		case MethodSimilar:
			best := 0.0
			for _, alias := range append([]string{m.Object.Name}, m.Object.Aliases...) {
				best = math.Max(best, catalog.Similarity(catalog.CoreName(alias), core))
			}
			if best >= minNameSimilarity {
				add(m.Object, MethodSimilar, best, whole, n)
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
	if len(subj.imaged) > 0 || len(subj.planned) > 0 {
		return s.matchFootprint(ctx, subj)
	}
	return s.matchNames(ctx, subj)
}

func (s *Service) nameHits(ctx context.Context, subj Subject) (map[string]*nameHit, map[string]catalog.Object) {
	names := map[string]*nameHit{}
	objs := map[string]catalog.Object{}
	rank := map[string]int{MethodDesignation: 3, MethodName: 2, MethodSimilar: 1}
	s.nameCandidates(ctx, subj, func(o catalog.Object, method string, sim float64, whole, part string) {
		n := names[o.ID]
		if n == nil || rank[method] > rank[n.method] || method == n.method && sim > n.sim {
			names[o.ID] = &nameHit{method: method, sim: sim, whole: whole, part: part}
			objs[o.ID] = o
		}
	})
	return names, objs
}

func (s *Service) matchNames(ctx context.Context, subj Subject) ([]Link, string) {
	reason, certain := s.notCatalogue(subj)
	if certain {
		return nil, reason
	}
	names, objs := s.nameHits(ctx, subj)
	if len(names) > 0 {
		reason = ""
	}
	if reason != "" {
		return nil, reason
	}
	links := make([]Link, 0, len(names))
	for id, n := range names {
		o := objs[id]
		l := Link{Subject: subj.Key, SubjectName: subj.Name, Object: o, Method: n.method, Basis: BasisName, NameMatch: n.method}
		if n.method == MethodSimilar {
			l.Similarity = round3p(n.sim)
		}
		l.Why = n.describe(o) + "." + n.composite()
		agree := objectRadius(o) + subj.Radius
		switch {
		case n.method == MethodSimilar:
			l.Status, l.Rule = StatusSuggested, RuleSimilarName
		case !subj.HasPos:
			l.Status, l.Rule = StatusAuto, RuleNameOnly
			l.Why += " No position to check it against."
		default:
			sep := catalog.Separation(subj.RA, subj.Dec, o.RA, o.Dec)
			l.Separation, l.AgreeRadius = round3p(sep), round3p(agree)
			if sep <= agree {
				l.Status, l.Rule = StatusAuto, RuleNameAtPlace
				l.Why += fmt.Sprintf(" Its centre is %s from your position, inside its own extent.", angle(sep))
			} else {
				l.Status, l.Rule = StatusSuggested, RuleNameFar
				l.Why += fmt.Sprintf(" But its centre is %s from your position, outside its %s extent.", angle(sep), angle(agree))
			}
		}
		if n.method == MethodSimilar && subj.HasPos {
			l.Separation = round3p(catalog.Separation(subj.RA, subj.Dec, o.RA, o.Dec))
		}
		links = append(links, l)
	}
	sortLinks(links)
	return links, ""
}

func linkRank(l Link) int {
	switch l.Status {
	case StatusConfirmed:
		return 0
	case StatusImaged:
		return 1
	case StatusAuto:
		return 2
	case StatusPlanned:
		return 3
	case StatusSuggested:
		return 4
	}
	return 5
}

func sortLinks(links []Link) {
	val := func(p *float64, def float64) float64 {
		if p == nil {
			return def
		}
		return *p
	}
	slices.SortStableFunc(links, func(a, b Link) int {
		if ra, rb := linkRank(a), linkRank(b); ra != rb {
			return ra - rb
		}
		if na, nb := a.NameMatch != "", b.NameMatch != ""; na != nb {
			if na {
				return -1
			}
			return 1
		}
		if ca, cb := val(a.Coverage, 0), val(b.Coverage, 0); ca != cb {
			if ca > cb {
				return -1
			}
			return 1
		}
		if sa, sb := val(a.Separation, math.Inf(1)), val(b.Separation, math.Inf(1)); sa != sb {
			if sa < sb {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Object.ID, b.Object.ID)
	})
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
	resolved := map[string]bool{}
	for _, l := range links {
		if (l.Status == StatusAuto || l.Status == StatusConfirmed) && l.NameMatch != "" {
			resolved[l.Subject] = true
		}
	}
	return slices.DeleteFunc(links, func(l Link) bool {
		return l.Status == StatusSuggested && l.Rule == RuleSimilarName && resolved[l.Subject]
	})
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
