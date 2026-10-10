package match

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const (
	MethodCoords      = "coords"
	MethodDesignation = "designation"
	MethodName        = "name"
	MethodFuzzyCoords = "fuzzy+coords"

	KindProject = "project"
	KindTarget  = "target"
	KindStack   = "stack"
)

const (
	coneFetchDeg       = 4.0
	minObjectRadiusDeg = 0.25
	coordsBase         = 0.35
	coordsScale        = 0.3
	farFactor          = 2.0
	farMinDeg          = 1.0
	farPenalty         = 0.5
	compositeFactor    = 0.9
	nameSearchLimit    = 25
	maxCoordsOnly      = 5
	maxPartialHits     = 5
)

type Candidate struct {
	Name      string
	RA        float64
	Dec       float64
	HasCoords bool
	Kind      string
}

type Result struct {
	Object     catalog.Object
	Method     string
	Confidence float64
	Evidence   []string
	Decision   string
}

func objectRadius(o catalog.Object) float64 {
	return math.Max(o.MajorArcmin/2/60, minObjectRadiusDeg)
}

func orCombine(a, b float64) float64 {
	return 1 - (1-a)*(1-b)
}

func formatDistance(d float64) string {
	if d >= 1 {
		return fmt.Sprintf("%.1f°", d)
	}
	return fmt.Sprintf("%.2f°", d)
}

type entry struct {
	obj        catalog.Object
	inCone     bool
	distance   float64
	coordsConf float64
	name       nameHit
	hasName    bool
	extra      []string
}

type accumulator struct {
	entries map[string]*entry
	order   []string
}

func (a *accumulator) get(o catalog.Object) *entry {
	key := o.Source + "\x00" + o.ID
	if e, ok := a.entries[key]; ok {
		return e
	}
	e := &entry{obj: o, distance: -1}
	a.entries[key] = e
	a.order = append(a.order, key)
	return e
}

func (a *accumulator) addName(o catalog.Object, h nameHit, extra string) {
	e := a.get(o)
	if e.hasName && e.name.conf >= h.conf {
		return
	}
	e.name, e.hasName = h, true
	e.extra = nil
	if extra != "" {
		e.extra = []string{extra}
	}
}

var compositeSplit = regexp.MustCompile(`(?i)\s*(?:&|\+|\band\b)\s*`)

func pluralSuffix(word string) (string, bool) {
	switch strings.ToLower(word) {
	case "nebulae":
		return "Nebula", true
	case "galaxies":
		return "Galaxy", true
	case "clusters":
		return "Cluster", true
	case "nebulas":
		return "Nebula", true
	}
	return "", false
}

func compositeParts(base string) []string {
	parts := compositeSplit.Split(base, -1)
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return nil
	}
	last := strings.Fields(out[len(out)-1])
	if singular, ok := pluralSuffix(last[len(last)-1]); ok {
		last[len(last)-1] = singular
		out[len(out)-1] = strings.Join(last, " ")
		for i := range out[:len(out)-1] {
			if len(strings.Fields(out[i])) == 1 {
				out[i] += " " + singular
			}
		}
	}
	return out
}

type scored struct {
	obj catalog.Object
	hit nameHit
}

func nameHits(ctx context.Context, store catalog.Store, base string) ([]scored, error) {
	query := base
	if d, _, ok := leadingDesignation(base); ok {
		query = d
	}
	objs, err := store.Search(ctx, query, nameSearchLimit)
	if err != nil {
		return nil, err
	}
	kept := make([]scored, 0, len(objs))
	strong := false
	for _, o := range objs {
		h, ok := nameEvidence(base, o, false)
		if !ok {
			continue
		}
		kept = append(kept, scored{o, h})
		if h.conf >= confCoreName {
			strong = true
		}
	}
	out := kept[:0]
	partials := 0
	for _, s := range kept {
		if s.hit.conf < confCoreName {
			if strong || partials >= maxPartialHits {
				continue
			}
			partials++
		}
		out = append(out, s)
	}
	return out, nil
}

func Match(ctx context.Context, store catalog.Store, c Candidate) ([]Result, error) {
	base, _ := StripPanel(c.Name)
	reason, certain, flagged := classify(base)
	if flagged && certain {
		return nil, &NotCatalogueError{Name: c.Name, Reason: reason}
	}
	acc := &accumulator{entries: make(map[string]*entry)}

	hits, err := nameHits(ctx, store, base)
	if err != nil {
		return nil, err
	}
	for _, s := range hits {
		acc.addName(s.obj, s.hit, "")
	}
	if len(hits) == 0 {
		for _, part := range compositeParts(base) {
			partHits, err := nameHits(ctx, store, part)
			if err != nil {
				return nil, err
			}
			for _, s := range partHits {
				h := s.hit
				h.conf *= compositeFactor
				acc.addName(s.obj, h, fmt.Sprintf("“%s” names more than one object; this is “%s”", strings.TrimSpace(base), part))
			}
		}
	}
	named := len(acc.order) > 0
	if c.HasCoords {
		if err := addCoords(ctx, store, c, base, acc); err != nil {
			return nil, err
		}
	}
	results := acc.results(c)
	if flagged && !named {
		if len(results) == 0 {
			return nil, &NotCatalogueError{Name: c.Name, Reason: reason}
		}
		for i := range results {
			results[i].Evidence = append(results[i].Evidence, fmt.Sprintf("“%s” is also a star’s name: %s", base, reason))
		}
	}
	return results, nil
}

func addCoords(ctx context.Context, store catalog.Store, c Candidate, base string, acc *accumulator) error {
	cone, err := store.Cone(ctx, c.RA, c.Dec, coneFetchDeg)
	if err != nil {
		return err
	}
	strongName := false
	for _, key := range acc.order {
		if e := acc.entries[key]; e.hasName && e.name.conf >= confCoreName {
			strongName = true
		}
	}
	for _, o := range cone {
		d := catalog.Separation(c.RA, c.Dec, o.RA, o.Dec)
		r := objectRadius(o)
		if d > r {
			continue
		}
		e := acc.get(o)
		e.inCone = true
		e.distance = d
		e.coordsConf = coordsBase + coordsScale*(1-d/r)
		if e.hasName || strongName {
			continue
		}
		if h, ok := nameEvidence(base, o, true); ok && h.method == MethodFuzzyCoords {
			e.name, e.hasName = h, true
		}
	}
	return nil
}

func (a *accumulator) results(c Candidate) []Result {
	out := make([]Result, 0, len(a.order))
	coordsOnly := 0
	for _, key := range a.order {
		e := a.entries[key]
		if !e.hasName && !e.inCone {
			continue
		}
		r := Result{Object: e.obj}
		if e.hasName {
			r.Method = e.name.method
			r.Confidence = e.name.conf
			r.Evidence = append(r.Evidence, e.name.evidence)
			r.Evidence = append(r.Evidence, e.extra...)
		}
		switch {
		case e.inCone:
			r.Evidence = append(r.Evidence, fmt.Sprintf("%s from the catalogue centre, inside its %s radius", formatDistance(e.distance), formatDistance(objectRadius(e.obj))))
			if e.hasName {
				r.Confidence = orCombine(r.Confidence, e.coordsConf)
			} else {
				r.Method = MethodCoords
				r.Confidence = e.coordsConf
			}
		case c.HasCoords && e.hasName:
			d := catalog.Separation(c.RA, c.Dec, e.obj.RA, e.obj.Dec)
			e.distance = d
			if d > math.Max(farFactor*objectRadius(e.obj), farMinDeg) {
				r.Confidence *= farPenalty
				r.Evidence = append(r.Evidence, fmt.Sprintf("but it is %s from the given coordinates", formatDistance(d)))
			} else {
				r.Evidence = append(r.Evidence, fmt.Sprintf("%s from the catalogue centre, just outside its %s radius", formatDistance(d), formatDistance(objectRadius(e.obj))))
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Object.Designation < out[j].Object.Designation
	})
	trimmed := out[:0]
	for _, r := range out {
		if r.Method == MethodCoords {
			if coordsOnly >= maxCoordsOnly {
				continue
			}
			coordsOnly++
		}
		trimmed = append(trimmed, r)
	}
	return trimmed
}

func Subject(kind, name string) string {
	return kind + ":" + name
}

func (c Candidate) Subject() string {
	return Subject(c.Kind, c.Name)
}
