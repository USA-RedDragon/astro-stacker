package main

import (
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

func isDesignation(s string) bool {
	_, ok := catalog.Canonical(s)
	return ok
}

func descriptive(name string) bool {
	words := strings.Fields(name)
	if len(words) >= 3 {
		return true
	}
	last := strings.ToLower(words[len(words)-1])
	for _, w := range []string{"nebula", "galaxy", "cluster", "cloud", "loop", "veil", "triplet", "quintet"} {
		if last == w {
			return true
		}
	}
	return false
}

func capitalise(name string) string {
	if name == "" || isDesignation(name) || !descriptive(name) {
		return name
	}
	r, n := utf8.DecodeRuneInString(name)
	if !unicode.IsLower(r) {
		return name
	}
	return string(unicode.ToUpper(r)) + name[n:]
}

func (b *builder) preferMessier(o *catalog.Object) {
	for i, a := range o.Aliases {
		c, ok := catalog.Canonical(a)
		if !ok || !strings.HasPrefix(c, "M ") || strings.HasPrefix(o.Designation, "M ") || b.lookup(c) != o {
			continue
		}
		o.Aliases[i] = o.Designation
		o.Designation = c
		return
	}
}

func rankFor(o *catalog.Object, name string) int {
	r := len(o.Lists) * 10
	if strings.HasPrefix(o.Designation, "M ") {
		r += 100
	}
	if catalog.NormalizeName(o.Name) == name {
		r += 5
	}
	return r
}

func (b *builder) dedupeNames() {
	owners := map[string][]*catalog.Object{}
	for _, o := range b.objects {
		seen := map[string]bool{}
		for _, n := range append([]string{o.Name}, o.Aliases...) {
			if n == "" || isDesignation(n) {
				continue
			}
			k := catalog.NormalizeName(n)
			if !seen[k] {
				seen[k] = true
				owners[k] = append(owners[k], o)
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(owners)) {
		objs := owners[k]
		if len(objs) < 2 {
			continue
		}
		winner := objs[0]
		for _, o := range objs[1:] {
			if r, w := rankFor(o, k), rankFor(winner, k); r > w || r == w && o.MajorArcmin > winner.MajorArcmin {
				winner = o
			}
		}
		for _, o := range objs {
			if o == winner {
				continue
			}
			o.Aliases = slices.DeleteFunc(o.Aliases, func(a string) bool { return !isDesignation(a) && catalog.NormalizeName(a) == k })
			if catalog.NormalizeName(o.Name) == k {
				o.Name = ""
				for _, a := range o.Aliases {
					if !isDesignation(a) && !strings.ContainsAny(a, "0123456789") {
						o.Name = a
						break
					}
				}
				if o.Name != "" {
					o.Aliases = slices.DeleteFunc(o.Aliases, func(a string) bool { return a == o.Name })
				}
			}
		}
	}
}

func (b *builder) finalize() {
	for _, o := range b.objects {
		o.Name = capitalise(o.Name)
		for i, a := range o.Aliases {
			o.Aliases[i] = capitalise(a)
		}
		b.preferMessier(o)
	}
	b.dedupeNames()
	b.byName = map[string]*catalog.Object{}
	for _, o := range b.objects {
		o.ID = catalog.Key(o.Designation)
		b.indexName(o, o.Name)
		for _, a := range o.Aliases {
			b.indexName(o, a)
		}
	}
}
