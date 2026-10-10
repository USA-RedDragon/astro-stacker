package discover

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const conflictSimilarity = 0.9

type nameHit struct {
	method string
	sim    float64
	whole  string
	part   string
}

func (n *nameHit) resolves() bool {
	return n != nil && (n.method == MethodDesignation || n.method == MethodName || n.method == MethodSimilar && n.sim >= conflictSimilarity)
}

func (n *nameHit) key() string {
	if n.part != "" {
		return n.part
	}
	return n.whole
}

func matchedAlias(o catalog.Object, name string) string {
	core := catalog.CoreName(name)
	best, bestSim := o.Name, -1.0
	for _, a := range append([]string{o.Name}, o.Aliases...) {
		if a == "" {
			continue
		}
		if sim := catalog.Similarity(catalog.CoreName(a), core); sim > bestSim {
			best, bestSim = a, sim
		}
	}
	return best
}

func (n *nameHit) describe(o catalog.Object) string {
	quoted := n.key()
	var what string
	switch n.method {
	case MethodDesignation:
		what = "its catalogue designation"
	case MethodName:
		what = "one of its names"
		if a := matchedAlias(o, quoted); a != "" {
			what += " (“" + a + "”)"
		}
	default:
		what = fmt.Sprintf("%.0f%% alike to its name", n.sim*100)
		if a := matchedAlias(o, quoted); a != "" {
			what += " “" + a + "”"
		}
	}
	return fmt.Sprintf("Your name “%s” is %s", quoted, what)
}

func (n *nameHit) composite() string {
	if n.whole == "" || n.part == "" {
		return ""
	}
	return fmt.Sprintf(" “%s” names more than one object; this is “%s”.", n.whole, n.part)
}

func targetList(ts []string) string {
	switch len(ts) {
	case 0:
		return ""
	case 1:
		return ts[0]
	case 2:
		return ts[0] + " and " + ts[1]
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ts[:2], ", "), len(ts)-2)
}

func coverText(c cover, o catalog.Object) string {
	if a, _ := semiAxes(o); a <= 0 {
		return "its position is inside"
	}
	where := "outside"
	if c.centre {
		where = "inside"
	}
	return fmt.Sprintf("%.0f%% of it covered, centre %s", c.fraction*100, where)
}

func bestKind(fields []field, targets []string) string {
	kind := ""
	rank := map[string]int{"": 0, FieldPointing: 1, FieldPlan: 2, FieldFrames: 3}
	for _, f := range fields {
		if slices.Contains(targets, f.target) && rank[f.kind] > rank[kind] {
			kind = f.kind
		}
	}
	return kind
}

func (s *Service) footprintCandidates(subj Subject, names map[string]*nameHit) []catalog.Object {
	fields := append(slices.Clone(subj.imaged), subj.planned...)
	var out []catalog.Object
	for _, o := range s.Catalog.All() {
		if len(o.Lists) == 0 && o.Name == "" && names[o.ID] == nil {
			continue
		}
		if names[o.ID] != nil || slices.ContainsFunc(fields, func(f field) bool { return reaches(f, o) }) {
			out = append(out, o)
		}
	}
	return out
}

func (s *Service) matchFootprint(ctx context.Context, subj Subject) ([]Link, string) {
	reason, certain := s.notCatalogue(subj)
	names := map[string]*nameHit{}
	if !certain {
		names, _ = s.nameHits(ctx, subj)
	}
	if reason != "" && !certain && len(names) > 0 {
		reason = ""
	}
	var links []Link
	explained := map[string]bool{}
	for _, o := range s.footprintCandidates(subj, names) {
		l, ok := s.evidenceLink(subj, o, names[o.ID])
		if !ok || reason != "" && l.Status == StatusSuggested {
			continue
		}
		if n := names[o.ID]; n != nil && l.Status != StatusSuggested {
			explained[n.key()] = true
		}
		links = append(links, l)
	}
	links = slices.DeleteFunc(links, func(l Link) bool {
		n := names[l.Object.ID]
		return l.Status == StatusSuggested && n != nil && explained[n.key()]
	})
	sortLinks(links)
	return links, reason
}

func (s *Service) evidenceLink(subj Subject, o catalog.Object, n *nameHit) (Link, bool) {
	l := Link{Subject: subj.Key, SubjectName: subj.Name, Object: o}
	if subj.HasPos {
		l.Separation = round3p(catalog.Separation(subj.RA, subj.Dec, o.RA, o.Dec))
	}
	if n != nil {
		l.NameMatch = n.method
		if n.method == MethodSimilar {
			l.Similarity = round3p(n.sim)
		}
	}
	ci := measureCover(o, subj.imaged)
	var cp cover
	if !ci.hit() {
		cp = measureCover(o, subj.planned)
	}
	coverage := func(c cover) *float64 {
		v := math.Round(c.fraction*100) / 100
		return &v
	}
	switch {
	case ci.hit():
		l.Method, l.Status, l.Rule, l.Basis, l.Targets = MethodFootprint, StatusImaged, RuleInsideFrames, BasisFrames, ci.targets
		l.CentreInside = ci.centre
		switch bestKind(subj.imaged, ci.targets) {
		case FieldPlan:
			l.Basis, l.Coverage = BasisTarget, coverage(ci)
			l.Why = fmt.Sprintf("Inside the Target Scheduler frame of %s, where your frames were taken (not plate solved yet): %s.", targetList(ci.targets), coverText(ci, o))
		case FieldPointing:
			l.Basis = BasisPointing
			l.Why = fmt.Sprintf("Your frames of %s point inside it; they have no plate solution, so how much of it they cover is unknown.", targetList(ci.targets))
		default:
			l.Coverage = coverage(ci)
			l.Why = fmt.Sprintf("Inside your frames of %s: %s.", targetList(ci.targets), coverText(ci, o))
		}
	case cp.hit():
		l.Method, l.Status, l.Rule, l.Basis, l.Targets = MethodFootprint, StatusPlanned, RuleInsidePlan, BasisPlan, cp.targets
		l.Coverage, l.CentreInside = coverage(cp), cp.centre
		l.Why = fmt.Sprintf("Inside the planned Target Scheduler frame of %s: %s. No frames yet.", targetList(cp.targets), coverText(cp, o))
	case n.resolves():
		l.Method, l.Status, l.Rule, l.Basis = n.method, StatusSuggested, RuleNameOutside, BasisName
		zero := 0.0
		l.Coverage = &zero
		where, nearest := "your frames", ci.nearest
		if len(subj.imaged) == 0 {
			where, nearest = "your planned frames", cp.nearest
		}
		l.Why = n.describe(o) + fmt.Sprintf(", but none of it is in %s", where)
		if nearest >= 0 {
			l.Why += fmt.Sprintf(": its centre is %s from the nearest frame centre", angle(nearest))
		}
		l.Why += "." + n.composite()
		return l, true
	default:
		return l, false
	}
	if n != nil {
		l.Why += " " + n.describe(o) + "." + n.composite()
	}
	return l, true
}
