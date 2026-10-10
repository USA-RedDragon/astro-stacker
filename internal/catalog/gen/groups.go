package main

import (
	_ "embed"
	"fmt"
	"math"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

//go:embed groups.tsv
var memberGroups string

const memberGroupsSource = "member-groups"

func (b *builder) groups() error {
	taken := map[string]bool{}
	for _, o := range b.objects {
		taken[o.ID] = true
	}
	var refs []string
	for line := range strings.SplitSeq(strings.TrimSpace(memberGroups), "\n") {
		p := strings.Split(line, "\t")
		if len(p) != 5 {
			return fmt.Errorf("groups.tsv: bad line %q", line)
		}
		if taken[p[0]] {
			return fmt.Errorf("groups.tsv: %s is already a catalogue object", p[0])
		}
		var ms []*catalog.Object
		var names []string
		for m := range strings.SplitSeq(p[3], ",") {
			o := b.lookup(m)
			if o == nil {
				return fmt.Errorf("groups.tsv: %s: no member %q", p[1], m)
			}
			ms = append(ms, o)
			names = append(names, m)
		}
		g := groupShape(ms)
		g.ID, g.Designation, g.Name, g.Type, g.Source, g.Members = p[0], p[1], p[1], catalog.TypeGalaxyGroup, memberGroupsSource, names
		if p[2] != "" {
			g.Aliases = strings.Split(p[2], ";")
		}
		b.objects = append(b.objects, g)
		taken[g.ID] = true
		refs = append(refs, p[1]+" "+p[4])
	}
	b.sources = append(b.sources, catalog.Source{
		ID: memberGroupsSource, Name: "astro-stacker galaxy groups computed from member lists",
		Citation: "Centre is the mean of the members' catalogue positions; the major axis lies along the members' principal axis and both axes enclose every member's catalogued major axis. Member lists from Wikipedia: " + strings.Join(refs, "; "),
		URL:      "https://github.com/USA-RedDragon/astro-stacker/blob/main/internal/catalog/gen/groups.tsv", Licence: "Member lists CC-BY-SA-4.0; positions and sizes per their catalogue",
	})
	return nil
}

func groupShape(ms []*catalog.Object) *catalog.Object {
	var x, y, z float64
	for _, m := range ms {
		cx, cy, cz := unit(m.RA, m.Dec)
		x, y, z = x+cx, y+cy, z+cz
	}
	ra, dec := fromUnit(x, y, z)
	xi := make([]float64, len(ms))
	eta := make([]float64, len(ms))
	var sxx, syy, sxy float64
	for i, m := range ms {
		xi[i], eta[i] = tangent(ra, dec, m.RA, m.Dec)
		sxx += xi[i] * xi[i]
		syy += eta[i] * eta[i]
		sxy += xi[i] * eta[i]
	}
	theta := 0.5 * math.Atan2(2*sxy, syy-sxx)
	s, c := math.Sin(theta), math.Cos(theta)
	var major, minor float64
	for i, m := range ms {
		r := m.MajorArcmin / 2
		u := xi[i]*s + eta[i]*c
		v := xi[i]*c - eta[i]*s
		major = math.Max(major, 2*(math.Abs(u)+r))
		minor = math.Max(minor, 2*(math.Abs(v)+r))
	}
	pa := math.Mod(theta*180/math.Pi+180, 180)
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	mn, p := round(minor), round(pa)
	return &catalog.Object{RA: ra, Dec: dec, MajorArcmin: round(major), MinorArcmin: &mn, PA: &p}
}

func tangent(ra0, dec0, ra, dec float64) (float64, float64) {
	const rad = math.Pi / 180
	a0, d0, a, d := ra0*rad, dec0*rad, ra*rad, dec*rad
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(a-a0)
	xi := math.Cos(d) * math.Sin(a-a0) / cosc
	eta := (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(a-a0)) / cosc
	return xi / rad * 60, eta / rad * 60
}
