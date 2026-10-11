package main

import (
	_ "embed"
	"fmt"
	"math"
	"strings"
)

//go:embed shapes.tsv
var shapeFixes string

func optional(s string) (*float64, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, true
	}
	v, ok := num(s)
	return &v, ok
}

func (b *builder) fixShapes() error {
	for line := range strings.SplitSeq(shapeFixes, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) != 7 {
			return fmt.Errorf("shapes.tsv: bad line %q", line)
		}
		o := b.lookup(p[0])
		if o == nil {
			return fmt.Errorf("shapes.tsv: no object %q", p[0])
		}
		ra, ok1 := hms(p[1], true)
		dec, ok2 := hms(p[2], false)
		major, ok3 := num(p[3])
		if !ok1 || !ok2 || !ok3 || major <= 0 {
			return fmt.Errorf("shapes.tsv: bad position or size in %q", line)
		}
		minor, ok4 := optional(p[4])
		pa, ok5 := optional(p[5])
		if !ok4 || !ok5 {
			return fmt.Errorf("shapes.tsv: bad minor axis or PA in %q", line)
		}
		if strings.TrimSpace(p[6]) == "" {
			return fmt.Errorf("shapes.tsv: %s has no source", p[0])
		}
		o.RA, o.Dec = math.Round(ra*1e5)/1e5, math.Round(dec*1e5)/1e5
		o.MajorArcmin, o.MinorArcmin, o.PA = major, minor, pa
	}
	return nil
}
