package main

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

//go:embed namefixes.tsv
var nameFixes string

const (
	opMove   = "move"
	opDrop   = "drop"
	opRename = "rename"
	opAlias  = "alias"
)

type nameFix struct {
	op, from, to, name string
}

func parseNameFixes() ([]nameFix, error) {
	var out []nameFix
	for line := range strings.SplitSeq(nameFixes, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p := strings.Split(line, "\t")
		switch {
		case p[0] == opMove && len(p) == 4:
			out = append(out, nameFix{op: p[0], from: p[1], to: p[2], name: p[3]})
		case (p[0] == opDrop || p[0] == opRename || p[0] == opAlias) && len(p) == 3:
			out = append(out, nameFix{op: p[0], from: p[1], name: p[2]})
		default:
			return nil, fmt.Errorf("namefixes.tsv: bad line %q", line)
		}
	}
	return out, nil
}

func dropName(o *catalog.Object, name string) {
	k := catalog.NormalizeName(name)
	if catalog.NormalizeName(o.Name) == k {
		o.Name = ""
	}
	o.Aliases = slices.DeleteFunc(o.Aliases, func(a string) bool { return catalog.NormalizeName(a) == k })
}

func setName(o *catalog.Object, name string) {
	dropName(o, name)
	if o.Name != "" {
		addAlias(o, o.Name)
	}
	o.Name = name
}

func addName(o *catalog.Object, name string) {
	if o.Name == "" {
		dropName(o, name)
		o.Name = name
		return
	}
	addAlias(o, name)
}

func dedupeNames(o *catalog.Object) {
	seen := map[string]bool{catalog.NormalizeName(o.Designation): true}
	if o.Name != "" {
		seen[catalog.NormalizeName(o.Name)] = true
	}
	o.Aliases = slices.DeleteFunc(o.Aliases, func(a string) bool {
		k := catalog.NormalizeName(a)
		if seen[k] {
			return true
		}
		seen[k] = true
		return false
	})
}

func applyNameFixes(fixes []nameFix, find func(string) *catalog.Object, strict bool) error {
	for _, f := range fixes {
		o := find(f.from)
		if o == nil {
			if strict {
				return fmt.Errorf("namefixes.tsv: no object %q", f.from)
			}
			continue
		}
		switch f.op {
		case opDrop:
			dropName(o, f.name)
		case opRename:
			setName(o, f.name)
		case opAlias:
			addName(o, f.name)
		case opMove:
			to := find(f.to)
			if to == nil {
				if strict {
					return fmt.Errorf("namefixes.tsv: no object %q", f.to)
				}
				continue
			}
			dropName(o, f.name)
			addName(to, f.name)
		}
	}
	return nil
}

func (b *builder) fixNames() error {
	fixes, err := parseNameFixes()
	if err != nil {
		return err
	}
	var catalogueFixes []nameFix
	for _, f := range fixes {
		if b.lookup(f.from) != nil {
			catalogueFixes = append(catalogueFixes, f)
		}
	}
	if err := applyNameFixes(catalogueFixes, b.lookup, true); err != nil {
		return err
	}
	for _, f := range catalogueFixes {
		k := catalog.NormalizeName(f.name)
		if f.op == opDrop {
			delete(b.byName, k)
			continue
		}
		target := f.from
		if f.op == opMove {
			target = f.to
		}
		if f.op == opAlias && b.byName[k] != nil {
			continue
		}
		b.byName[k] = b.lookup(target)
	}
	for _, o := range b.objects {
		dedupeNames(o)
	}
	return nil
}

func fixOverlayNames(ov *overlay) error {
	fixes, err := parseNameFixes()
	if err != nil {
		return err
	}
	byKey := map[string]*catalog.Object{}
	for i := range ov.Objects {
		o := &ov.Objects[i]
		byKey[catalog.Key(o.Designation)] = o
	}
	if err := applyNameFixes(fixes, func(d string) *catalog.Object { return byKey[catalog.Key(d)] }, false); err != nil {
		return err
	}
	for i := range ov.Objects {
		dedupeNames(&ov.Objects[i])
	}
	drops := map[string]map[string]bool{}
	for _, f := range fixes {
		if f.op == opMove || f.op == opDrop {
			k := catalog.Key(f.from)
			if drops[k] == nil {
				drops[k] = map[string]bool{}
			}
			drops[k][catalog.NormalizeName(f.name)] = true
		}
	}
	for i := range ov.Patches {
		p := &ov.Patches[i]
		d := drops[p.ID]
		if d == nil {
			continue
		}
		if d[catalog.NormalizeName(p.Name)] {
			p.Name = ""
		}
		p.Aliases = slices.DeleteFunc(p.Aliases, func(a string) bool { return d[catalog.NormalizeName(a)] })
	}
	return nil
}
