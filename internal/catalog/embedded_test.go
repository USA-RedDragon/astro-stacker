package catalog_test

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

func defaultIndex(t *testing.T) *catalog.Index {
	t.Helper()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestEmbeddedLists(t *testing.T) {
	t.Parallel()
	ix := defaultIndex(t)
	want := map[string]int{messier: 110, "caldwell": 109, "herschel400": 400, "sharpless": 313, "vdb": 158, "arp": 338, "hickson": 100}
	got := map[string]int{}
	for _, l := range ix.Lists() {
		got[l.Key] = l.Total
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("list %s has %d objects, want %d", k, got[k], n)
		}
	}
	seen := map[string]bool{}
	for _, o := range ix.List(messier) {
		if seen[o.ID] {
			t.Errorf("Messier list repeats %s", o.ID)
		}
		seen[o.ID] = true
	}
	if len(ix.Sources()) < 5 {
		t.Errorf("only %d sources recorded", len(ix.Sources()))
	}
	for _, s := range ix.Sources() {
		if s.Licence == "" || s.Citation == "" {
			t.Errorf("source %s has no licence or citation", s.ID)
		}
	}
}

func TestEmbeddedSearch(t *testing.T) {
	t.Parallel()
	ix := defaultIndex(t)
	ctx := context.Background()
	cases := map[string]string{
		"Garlic Nebula": garlic, abell85: garlic, m31: m31, "M102": "M 102",
		"SH2-129": "Sh2-129", "Centarus A": "NGC 5128", "Cygnis Loop": "G074.0-08.5", c14: c14,
		"Horsehead": "B 33", "Pacman": "NGC 281", "NGC1313": "NGC 1313",
	}
	for q, want := range cases {
		res, err := ix.Search(ctx, q, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) == 0 || res[0].Designation != want {
			got := ""
			if len(res) > 0 {
				got = res[0].Designation
			}
			t.Errorf("Search(%q) first = %q, want %q", q, got, want)
		}
	}
	if res, _ := ix.Search(ctx, "   ", 5); len(res) != 0 {
		t.Error("blank query found objects")
	}
}

func TestEmbeddedCone(t *testing.T) {
	t.Parallel()
	ix := defaultIndex(t)
	res, err := ix.Cone(context.Background(), 23.986944*15, 62.436667, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].Designation != garlic {
		t.Fatalf("cone at the Garlic Nebula found %v", res)
	}
	for _, o := range res {
		if catalog.Separation(359.8, 62.44, o.RA, o.Dec) > 0.31 {
			t.Errorf("%s is outside the cone", o.Designation)
		}
	}
	var _ catalog.Store = ix
}

func TestSeparation(t *testing.T) {
	t.Parallel()
	if d := catalog.Separation(10, 20, 10, 21); math.Abs(d-1) > 1e-9 {
		t.Errorf("1 degree of dec = %v", d)
	}
	if d := catalog.Separation(0, 0, 180, 0); math.Abs(d-180) > 1e-9 {
		t.Errorf("antipode = %v", d)
	}
	if d := catalog.Separation(359.9, 0, 0.1, 0); math.Abs(d-0.2) > 1e-9 {
		t.Errorf("across RA 0 = %v", d)
	}
}

const (
	ngc1     = "NGC1"
	ngc1Name = "NGC 1"
)

func TestNewIndexSynthetic(t *testing.T) {
	t.Parallel()
	ix := catalog.NewIndex(catalog.Dataset{
		Lists: map[string][]string{messier: {ngc1}},
		Objects: []catalog.Object{
			{ID: ngc1, Designation: ngc1Name, Name: "Test Nebula", Aliases: []string{"M 1"}, Type: "emission", RA: 10, Dec: 10, Lists: []string{messier}},
			{ID: "NGC2", Designation: "NGC 2", Type: "galaxy", RA: 11, Dec: 10},
		},
	})
	if o, ok := ix.Lookup("Messier 1"); !ok || o.ID != ngc1 {
		t.Errorf("Lookup by alias = %v %v", o, ok)
	}
	if o, ok := ix.Get("NGC2"); !ok || o.Type != "galaxy" {
		t.Errorf("Get = %v %v", o, ok)
	}
	if _, ok := ix.Get("NGC3"); ok {
		t.Error("Get found a missing object")
	}
	if l := ix.List(messier); len(l) != 1 {
		t.Errorf("List = %v", l)
	}
	m := ix.Find(context.Background(), "test", 5)
	if len(m) != 1 || m[0].How != "name" && m[0].How != "prefix" {
		t.Errorf("Find = %+v", m)
	}
}

func TestApplyOverlay(t *testing.T) {
	t.Parallel()
	sb := 22.5
	ds := catalog.Dataset{Objects: []catalog.Object{{ID: ngc1, Designation: ngc1Name, Type: catalog.TypeGalaxy}}}
	ds.Apply(catalog.Overlay{
		Source:  catalog.Source{ID: "nina-atlas", Licence: "MPL-2.0"},
		Patches: []catalog.Patch{{ID: ngc1, Name: "Test Galaxy", Aliases: []string{"UGC 57", ngc1Name}, SurfaceBrightness: &sb, MajorArcmin: 2}, {ID: "missing"}},
		Objects: []catalog.Object{{ID: "PK106-17.1", Designation: "PK 106-17.1"}, {ID: ngc1, Designation: ngc1Name}},
	})
	if len(ds.Objects) != 2 || len(ds.Sources) != 1 {
		t.Fatalf("objects %d sources %d", len(ds.Objects), len(ds.Sources))
	}
	o := ds.Objects[0]
	if o.Name != "Test Galaxy" || len(o.Aliases) != 1 || o.SurfaceBrightness == nil || o.MajorArcmin != 2 {
		t.Errorf("patched object %+v", o)
	}
	ix := catalog.NewIndex(ds)
	if got, ok := ix.Lookup("PK 106-17.1"); !ok || got.ID != "PK106-17.1" {
		t.Errorf("lookup of a non-canonical designation: %v %v", got, ok)
	}
}

func TestEmbeddedAtlasOverlay(t *testing.T) {
	t.Parallel()
	ix := defaultIndex(t)
	for q, want := range map[string]string{"Zwicky's Triplet": "Arp 103", "Coma Cluster": "ACO 1656", "Eagle Nebula": "M 16"} {
		res := ix.Find(context.Background(), q, 1)
		if len(res) == 0 || res[0].Object.Designation != want {
			t.Errorf("Find(%q) = %+v, want %s", q, res, want)
		}
	}
	for _, d := range []string{"ESO 434-6", "PK 106-17.1", "Ruprecht 44", "Mrk 348"} {
		if _, ok := ix.Lookup(d); !ok {
			t.Errorf("Lookup(%q) failed", d)
		}
	}
	if res := ix.Find(context.Background(), "Eagle Nebula", 5); len(res) > 1 && res[1].How == "name" {
		t.Errorf("Eagle Nebula names two objects: %s and %s", res[0].Object.Designation, res[1].Object.Designation)
	}
	if o, _ := ix.Lookup("M 17"); slices.ContainsFunc(append(o.Aliases, o.Name), func(a string) bool { return a != "" && a[0] >= 'a' && a[0] <= 'z' && strings.HasSuffix(a, "Nebula") }) {
		t.Errorf("M 17 has a lowercase name: %v", o.Aliases)
	}
	if o, _ := ix.Lookup("NGC 224"); o.Designation != m31 || o.ID != "M31" {
		t.Errorf("NGC 224 is %s (%s), want the Messier designation", o.Designation, o.ID)
	}
}
