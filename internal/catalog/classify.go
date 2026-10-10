package catalog

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

const (
	classMatchFloorDeg = 0.1
	classMatchMaxRatio = 4.0
)

type classEvidence struct {
	typ   string
	desig string
	what  string
	sepAM float64
	self  bool
}

func (e classEvidence) text() string {
	s := fmt.Sprintf("listed as %s (%s)", e.desig, e.what)
	if !e.self {
		s += fmt.Sprintf(", %.1f′ from its centre", e.sepAM)
	}
	return s
}

func designationEvidence(d string) (classEvidence, bool) {
	switch {
	case strings.HasPrefix(d, "vdB "):
		return classEvidence{typ: TypeReflection, desig: d, what: "van den Bergh 1966, a catalogue of reflection nebulae"}, true
	case strings.HasPrefix(d, "Sh2-"):
		return classEvidence{typ: TypeEmission, desig: d, what: "Sharpless 1959, a catalogue of H II regions"}, true
	case strings.HasPrefix(d, "RCW "):
		return classEvidence{typ: TypeEmission, desig: d, what: "RCW 1960, a catalogue of H-α emission regions"}, true
	}
	return classEvidence{}, false
}

func ownEvidence(o *Object) []classEvidence {
	var out []classEvidence
	for _, d := range append([]string{o.Designation}, o.Aliases...) {
		if e, ok := designationEvidence(d); ok {
			e.self = true
			out = append(out, e)
		}
	}
	return out
}

func typedEvidence(o *Object) (classEvidence, bool) {
	if o.Type != TypeEmission && o.Type != TypeReflection {
		return classEvidence{}, false
	}
	for _, e := range ownEvidence(o) {
		if e.typ == o.Type {
			return e, true
		}
	}
	return classEvidence{}, false
}

func sameNebula(a, b *Object) (float64, bool) {
	d := Separation(a.RA, a.Dec, b.RA, b.Dec)
	if a.MajorArcmin <= 0 || b.MajorArcmin <= 0 {
		return d * 60, d <= classMatchFloorDeg
	}
	small, large := math.Min(a.MajorArcmin, b.MajorArcmin)/120, math.Max(a.MajorArcmin, b.MajorArcmin)/120
	if d <= small {
		return d * 60, true
	}
	if large/small > classMatchMaxRatio {
		return 0, false
	}
	return d * 60, d <= math.Max(large, classMatchFloorDeg)
}

func nebulaSourceName(src string, sources []Source) string {
	for _, s := range sources {
		if s.ID == src {
			return s.Name
		}
	}
	return src
}

// ResolveNebulae settles objects typed only "nebula" as emission or
// reflection from the van den Bergh, Sharpless and RCW entries for the same
// nebula, and records the basis in TypeBasis. An object with no
// such entry, or with entries of both kinds, stays "nebula" and TypeBasis
// says why.
func ResolveNebulae(ds *Dataset) {
	type witness struct {
		i int
		e classEvidence
	}
	var witnesses []witness
	for i := range ds.Objects {
		if e, ok := typedEvidence(&ds.Objects[i]); ok {
			witnesses = append(witnesses, witness{i, e})
		}
	}
	for i := range ds.Objects {
		o := &ds.Objects[i]
		if o.Type != TypeNebula {
			continue
		}
		ev := ownEvidence(o)
		for _, w := range witnesses {
			if w.i == i {
				continue
			}
			if sep, ok := sameNebula(o, &ds.Objects[w.i]); ok {
				e := w.e
				e.sepAM, e.self = sep, false
				ev = append(ev, e)
			}
		}
		slices.SortStableFunc(ev, func(a, b classEvidence) int {
			if a.self != b.self {
				if a.self {
					return -1
				}
				return 1
			}
			return cmp.Compare(a.sepAM, b.sepAM)
		})
		resolveNebula(o, ev, ds.Sources)
	}
}

func resolveNebula(o *Object, ev []classEvidence, sources []Source) {
	byType := map[string][]classEvidence{}
	for _, e := range ev {
		byType[e.typ] = append(byType[e.typ], e)
	}
	em, rf := byType[TypeEmission], byType[TypeReflection]
	switch {
	case len(em) > 0 && len(rf) > 0:
		o.TypeBasis = fmt.Sprintf("emission or reflection is not settled: it is %s, and %s", em[0].text(), rf[0].text())
	case len(em) > 0:
		o.Type, o.TypeBasis = TypeEmission, evidenceText(em)
	case len(rf) > 0:
		o.Type, o.TypeBasis = TypeReflection, evidenceText(rf)
	default:
		o.TypeBasis = fmt.Sprintf("emission or reflection is not known: %s types it only as a nebula, and no van den Bergh, Sharpless or RCW entry matches it",
			nebulaSourceName(o.Source, sources))
	}
}

func evidenceText(ev []classEvidence) string {
	parts := make([]string, 0, 2)
	for _, e := range ev[:min(len(ev), 2)] {
		parts = append(parts, e.text())
	}
	return strings.Join(parts, "; ")
}
