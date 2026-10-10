package match

import (
	"fmt"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const (
	confDesignation        = 0.9
	confLeadingDesignation = 0.8
	confExactName          = 0.85
	confCoreName           = 0.75
	confPartialName        = 0.4
	fuzzyBase              = 0.4
	fuzzyScale             = 0.4
	minFuzzySimilarity     = 0.45
	maxLetterEdits         = 3
	minPartialLength       = 4
	maxLeadingTokens       = 3
)

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func letters(n int) string {
	switch n {
	case 1:
		return "one letter"
	case 2:
		return "two letters"
	case 3:
		return "three letters"
	}
	return fmt.Sprintf("%d letters", n)
}

func displayFields(original, normalized string) []string {
	o := strings.Fields(original)
	n := strings.Fields(normalized)
	if len(o) == len(n) {
		return o
	}
	return n
}

func fuzzyCompare(query, name string) (float64, string, bool) {
	cq, cn := catalog.CoreName(query), catalog.CoreName(name)
	if cq == "" || cn == "" || cq == cn {
		return 0, "", false
	}
	sim := catalog.Similarity(cq, cn)
	if sim < minFuzzySimilarity {
		return 0, "", false
	}
	nq, nn := catalog.NormalizeName(query), catalog.NormalizeName(name)
	qt, nt := strings.Fields(nq), strings.Fields(nn)
	if len(qt) == len(nt) {
		qd, nd := displayFields(query, nq), displayFields(name, nn)
		var diffs []string
		for i := range qt {
			if qt[i] != nt[i] {
				diffs = append(diffs, fmt.Sprintf("“%s” is %s from “%s”", qd[i], letters(levenshtein(qt[i], nt[i])), nd[i]))
			}
		}
		if len(diffs) > 0 {
			return sim, strings.Join(diffs, "; "), true
		}
	}
	q := strings.TrimSpace(query)
	switch edits := levenshtein(nq, nn); {
	case strings.HasPrefix(nn, nq):
		return sim, fmt.Sprintf("“%s” abbreviates “%s”", q, name), true
	case edits <= maxLetterEdits:
		return sim, fmt.Sprintf("“%s” is %s from “%s”", q, letters(edits), name), true
	}
	return sim, fmt.Sprintf("“%s” resembles “%s”", q, name), true
}

func objectNames(o catalog.Object) []string {
	out := make([]string, 0, len(o.Aliases)+2)
	if o.Name != "" {
		out = append(out, o.Name)
	}
	if o.Designation != "" {
		out = append(out, o.Designation)
	}
	return append(out, o.Aliases...)
}

func leadingDesignation(base string) (string, bool, bool) {
	if d, ok := NormalizeDesignation(base); ok {
		return d, true, true
	}
	tokens := strings.Fields(base)
	for k := min(maxLeadingTokens, len(tokens)-1); k >= 1; k-- {
		if d, ok := NormalizeDesignation(strings.Join(tokens[:k], " ")); ok {
			return d, false, true
		}
	}
	return "", false, false
}

type nameHit struct {
	conf     float64
	method   string
	evidence string
}

func designationHit(base string, o catalog.Object) (nameHit, bool) {
	d, full, ok := leadingDesignation(base)
	if !ok {
		return nameHit{}, false
	}
	key := DesignationKey(d)
	for _, n := range objectNames(o) {
		if DesignationKey(n) != key {
			continue
		}
		if full {
			return nameHit{confDesignation, MethodDesignation, "name matches designation " + n}, true
		}
		return nameHit{confLeadingDesignation, MethodDesignation, "name begins with designation " + n}, true
	}
	return nameHit{}, false
}

func nameEvidence(base string, o catalog.Object, allowFuzzy bool) (nameHit, bool) {
	if h, ok := designationHit(base, o); ok {
		return h, true
	}
	if _, full, ok := leadingDesignation(base); ok && full {
		return nameHit{}, false
	}
	nb := catalog.NormalizeName(base)
	if nb == "" {
		return nameHit{}, false
	}
	names := objectNames(o)
	for _, n := range names {
		if catalog.NormalizeName(n) == nb {
			return nameHit{confExactName, MethodName, fmt.Sprintf("name matches the catalogue name “%s”", n)}, true
		}
	}
	core := catalog.CoreName(base)
	if core != "" {
		for _, n := range names {
			if catalog.CoreName(n) == core {
				return nameHit{confCoreName, MethodName, fmt.Sprintf("“%s” matches the catalogue name “%s”", strings.TrimSpace(base), n)}, true
			}
		}
	}
	if allowFuzzy {
		best := nameHit{}
		for _, n := range names {
			if sim, ev, ok := fuzzyCompare(base, n); ok && fuzzyBase+fuzzyScale*sim > best.conf {
				best = nameHit{fuzzyBase + fuzzyScale*sim, MethodFuzzyCoords, ev}
			}
		}
		if best.conf > 0 {
			return best, true
		}
	}
	if len(nb) >= minPartialLength {
		for _, n := range names {
			nn := catalog.NormalizeName(n)
			if len(nn) < minPartialLength {
				continue
			}
			if strings.Contains(" "+nn+" ", " "+nb+" ") {
				return nameHit{confPartialName, MethodName, fmt.Sprintf("name is part of “%s”", n)}, true
			}
			if strings.Contains(" "+nb+" ", " "+nn+" ") {
				return nameHit{confPartialName, MethodName, fmt.Sprintf("name contains “%s”", n)}, true
			}
		}
	}
	return nameHit{}, false
}
