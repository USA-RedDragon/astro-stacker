package match

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

var pkPattern = regexp.MustCompile(`(?i)^\s*pk\s*-?\s*0*(\d+)\s*([+-])\s*0*(\d+)(?:\.(\d+))?\s*$`)

var acoPattern = regexp.MustCompile(`(?i)^\s*aco\b`)

var colPattern = regexp.MustCompile(`(?i)^\s*col\b`)

var trailingNumber = regexp.MustCompile(`(\d+)\D*$`)

func NormalizeDesignation(s string) (string, bool) {
	if m := pkPattern.FindStringSubmatch(s); m != nil {
		out := "PK " + m[1] + m[2] + m[3]
		if m[4] != "" {
			out += "." + m[4]
		}
		return out, true
	}
	s = acoPattern.ReplaceAllString(s, "Abell")
	s = colPattern.ReplaceAllString(s, "Cr")
	return catalog.Canonical(s)
}

func DesignationKey(s string) string {
	if d, ok := NormalizeDesignation(s); ok {
		return catalog.Key(d)
	}
	return catalog.Key(s)
}

func StripPanel(name string) (string, int) {
	trimmed := strings.TrimSpace(name)
	base := catalog.StripPanel(trimmed)
	if base == "" || base == trimmed {
		return trimmed, 0
	}
	m := trailingNumber.FindStringSubmatch(trimmed[len(base):])
	if m == nil {
		return base, 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return base, 0
	}
	return base, n
}
