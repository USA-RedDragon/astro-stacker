package catalog_test

import (
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const srcLBN = "vii-9-catalog"

func TestResolveNebulaeEmbedded(t *testing.T) {
	t.Parallel()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, typ, basis string }{
		{"NGC 7023", catalog.TypeReflection, "listed as vdB 139 (van den Bergh 1966, a catalogue of reflection nebulae), 0.4′ from its centre"},
		{"M 17", catalog.TypeEmission, "listed as Sh2-45 (Sharpless 1959, a catalogue of H II regions), 0.3′ from its centre"},
		{"IC 405", catalog.TypeNebula, "emission or reflection is not settled: it is listed as Sh2-229"},
		{"IC 4628", catalog.TypeNebula, "emission or reflection is not known: OpenNGC v20260501 types it only as a nebula"},
		{"M 57", "", ""},
	} {
		o, ok := ix.Get(c.id)
		if !ok {
			t.Fatalf("%s missing", c.id)
		}
		if c.typ == "" {
			if o.TypeBasis != "" {
				t.Errorf("%s is not typed nebula, basis %q", c.id, o.TypeBasis)
			}
			continue
		}
		if o.Type != c.typ || !strings.HasPrefix(o.TypeBasis, c.basis) {
			t.Errorf("%s = %s %q, want %s %q", c.id, o.Type, o.TypeBasis, c.typ, c.basis)
		}
	}
}

func TestResolveNebulaeMatching(t *testing.T) {
	t.Parallel()
	ds := catalog.Dataset{
		Sources: []catalog.Source{{ID: srcLBN, Name: "Lynds 1965"}},
		Objects: []catalog.Object{
			{ID: "VDB1", Designation: "vdB 3", Type: catalog.TypeReflection, RA: 10, Dec: 10, MajorArcmin: 10},
			{ID: "SH2-1", Designation: "Sh2-1", Type: catalog.TypeEmission, RA: 50, Dec: 10, MajorArcmin: 120},
			{ID: "RCW1", Designation: "RCW 1", Type: catalog.TypeEmission, RA: 80, Dec: 10, MajorArcmin: 20},
			{ID: "VDB2", Designation: "vdB 2", Type: catalog.TypeReflection, RA: 80, Dec: 10.02, MajorArcmin: 20},
			{ID: "SAME", Designation: "LBN 1", Type: catalog.TypeNebula, RA: 10, Dec: 10.01, MajorArcmin: 8, Source: srcLBN},
			{ID: "INSIDE", Designation: "LBN 2", Type: catalog.TypeNebula, RA: 50, Dec: 10.5, MajorArcmin: 10, Source: srcLBN},
			{ID: "CORE", Designation: "LBN 3", Type: catalog.TypeNebula, RA: 50, Dec: 10.02, MajorArcmin: 10, Source: srcLBN},
			{ID: "NOSIZE", Designation: "LBN 4", Type: catalog.TypeNebula, RA: 50, Dec: 10.3, Source: srcLBN},
			{ID: "BOTH", Designation: "LBN 5", Type: catalog.TypeNebula, RA: 80, Dec: 10.01, MajorArcmin: 20, Source: srcLBN},
			{ID: "SELF", Designation: "LBN 6", Aliases: []string{"vdB 9"}, Type: catalog.TypeNebula, RA: 200, Dec: 0, Source: srcLBN},
		},
	}
	catalog.ResolveNebulae(&ds)
	got := map[string]catalog.Object{}
	for _, o := range ds.Objects {
		got[o.ID] = o
	}
	for id, want := range map[string]string{
		"SAME": catalog.TypeReflection, "INSIDE": catalog.TypeNebula, "CORE": catalog.TypeEmission,
		"NOSIZE": catalog.TypeNebula, "BOTH": catalog.TypeNebula, "SELF": catalog.TypeReflection,
	} {
		if got[id].Type != want || got[id].TypeBasis == "" {
			t.Errorf("%s = %s %q, want %s", id, got[id].Type, got[id].TypeBasis, want)
		}
	}
	if b := got["SELF"].TypeBasis; b != "listed as vdB 9 (van den Bergh 1966, a catalogue of reflection nebulae)" {
		t.Errorf("self basis %q", b)
	}
	if b := got["NOSIZE"].TypeBasis; b != "emission or reflection is not known: Lynds 1965 types it only as a nebula, and no van den Bergh, Sharpless or RCW entry matches it" {
		t.Errorf("unknown basis %q", b)
	}
	if b := got["BOTH"].TypeBasis; !strings.Contains(b, "not settled") || !strings.Contains(b, "RCW 1") || !strings.Contains(b, "vdB 2") {
		t.Errorf("conflict basis %q", b)
	}
	if got["VDB1"].TypeBasis != "" {
		t.Error("a typed entry was given a basis")
	}
}
