package match_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog/match"
)

type fakeStore struct {
	objects []catalog.Object
}

func (f fakeStore) Cone(_ context.Context, ra, dec, radius float64) ([]catalog.Object, error) {
	var out []catalog.Object
	for _, o := range f.objects {
		if catalog.Separation(ra, dec, o.RA, o.Dec) <= radius {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b catalog.Object) int {
		da, db := catalog.Separation(ra, dec, a.RA, a.Dec), catalog.Separation(ra, dec, b.RA, b.Dec)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	return out, nil
}

func (f fakeStore) Search(_ context.Context, _ string, limit int) ([]catalog.Object, error) {
	if limit < len(f.objects) {
		return f.objects[:limit], nil
	}
	return f.objects, nil
}

func obj(designation, name string, ra, dec, major float64, aliases ...string) catalog.Object {
	return catalog.Object{
		ID: designation, Designation: designation, Name: name, RA: ra, Dec: dec,
		MajorArcmin: major, MinorArcmin: major, Aliases: aliases, Source: "fake",
	}
}

func fake() fakeStore {
	return fakeStore{objects: []catalog.Object{
		obj(m31, "Andromeda Galaxy", 10.6847, 41.2688, 189, "NGC 224", "Andromeda Nebula"),
		obj(sh2103, "Cygnus Loop", 312.75, 30.7, 210),
		obj("NGC 6960", "Veil Nebula", 311.4, 30.71, 70, c34, "Western Veil"),
		obj(garlicID, garlicName, 359.7917, 62.4333, 34, "CTB 1"),
		obj(ngc5128, "Centaurus A", 201.365, -43.019, 25.7),
		obj("M 16", "Eagle Nebula", 274.7, -13.807, 7, "NGC 6611"),
		obj("M 17", "Omega Nebula", 275.196, -16.172, 11, "NGC 6618", "Swan Nebula"),
		obj(sh2129, "Flying Bat Nebula", 317.937, 59.961, 140),
		obj(ngc1313, "", 49.567, -66.498, 9.2),
		obj(ic4604, "Rho Ophiuchi Nebula", 246.5, -23.4, 60, "LBN 1111"),
		obj(gum3, "", 105.57, -12.25, 30),
		obj("NGC 3372", "Eta Carinae Nebula", 161.265, -59.867, 120, "C 92"),
		obj("NGC 2237", "Rosette Nebula", 97.98, 4.94, 80, "C 49"),
		obj("IC 1318", "Sadr Region", 305.4, 40.4, 240),
	}}
}

func top(t *testing.T, results []match.Result) match.Result {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("no results")
	}
	return results[0]
}

func hasEvidence(r match.Result, substr string) bool {
	for _, e := range r.Evidence {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func TestMatchByName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		want     string
		method   string
		evidence string
		minConf  float64
	}{
		{m31Spelled, m31, match.MethodDesignation, "name matches designation M 31", 0.85},
		{"m 31", m31, match.MethodDesignation, "designation M 31", 0.85},
		{"NGC 224", m31, match.MethodDesignation, "designation NGC 224", 0.85},
		{"Andromeda Galaxy", m31, match.MethodName, "“Andromeda Galaxy”", 0.8},
		{andromeda, m31, match.MethodName, andromeda, 0.7},
		{sh2129Spelled, sh2129, match.MethodDesignation, "name matches designation Sh2-129", 0.85},
		{"Sh2 129", sh2129, match.MethodDesignation, sh2129, 0.85},
		{ngc1313Spelled, ngc1313, match.MethodDesignation, ngc1313, 0.85},
		{"gum 3", gum3, match.MethodDesignation, gum3, 0.85},
		{ic4604Panel6, ic4604, match.MethodDesignation, ic4604, 0.85},
		{garlicName, garlicID, match.MethodName, garlicName, 0.8},
		{"CTB 1", garlicID, match.MethodName, "CTB 1", 0.8},
		{"Rosette", "NGC 2237", match.MethodName, "Rosette Nebula", 0.7},
		{"Eta Carinae", "NGC 3372", match.MethodName, "Eta Carinae Nebula", 0.7},
		{"NGC 7000 North America", "", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			results, err := match.Match(context.Background(), fake(), match.Candidate{Name: tc.name, Kind: match.KindTarget})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(results) != 0 {
					t.Fatalf("%q matched %v", tc.name, results)
				}
				return
			}
			r := top(t, results)
			if r.Object.Designation != tc.want || r.Method != tc.method || r.Confidence < tc.minConf {
				t.Fatalf("%q: got %s via %s at %.2f (%v)", tc.name, r.Object.Designation, r.Method, r.Confidence, r.Evidence)
			}
			if !hasEvidence(r, tc.evidence) {
				t.Fatalf("%q: evidence %v lacks %q", tc.name, r.Evidence, tc.evidence)
			}
		})
	}
}

func TestMatchWithCoordinates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		ra, dec  float64
		want     string
		method   string
		evidence []string
		minConf  float64
	}{
		{garlicName, 23.987 * 15, 62.44, garlicID, match.MethodName, []string{garlicName, fromCentre}, 0.9},
		{"Cygnis Loop Panel 2", 312.2, 31.2, sh2103, match.MethodFuzzyCoords, []string{"“Cygnis” is one letter from “Cygnus”", fromCentre}, 0.8},
		{centarusA, 201.37, -43.02, ngc5128, match.MethodFuzzyCoords, []string{"“Centarus” is one letter from “Centaurus”"}, 0.8},
		{sh2129Spelled, 317.9, 60.0, sh2129, match.MethodDesignation, []string{"name matches designation Sh2-129", "0.04° from the catalogue centre"}, 0.95},
		{"My Mystery Field", 10.7, 41.3, m31, match.MethodCoords, []string{fromCentre}, 0.5},
		{"Eta Car", 161.265, -59.68, "NGC 3372", match.MethodFuzzyCoords, []string{"“Eta Car” abbreviates “Eta Carinae Nebula”"}, 0.7},
		{"Gamma Cygni", 305.557, 40.257, "IC 1318", match.MethodCoords, []string{"“Gamma Cygni” is also a star’s name"}, 0.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			results, err := match.Match(context.Background(), fake(), match.Candidate{Name: tc.name, RA: tc.ra, Dec: tc.dec, HasCoords: true, Kind: match.KindProject})
			if err != nil {
				t.Fatal(err)
			}
			r := top(t, results)
			if r.Object.Designation != tc.want || r.Method != tc.method || r.Confidence < tc.minConf {
				t.Fatalf("%q: got %s via %s at %.2f (%v)", tc.name, r.Object.Designation, r.Method, r.Confidence, r.Evidence)
			}
			for _, e := range tc.evidence {
				if !hasEvidence(r, e) {
					t.Fatalf("%q: evidence %v lacks %q", tc.name, r.Evidence, e)
				}
			}
			for i := 1; i < len(results); i++ {
				if results[i].Confidence > results[i-1].Confidence {
					t.Fatalf("results not sorted by confidence: %v", results)
				}
			}
		})
	}
}

func TestCoordinatesRaiseConfidence(t *testing.T) {
	t.Parallel()
	byName, err := match.Match(context.Background(), fake(), match.Candidate{Name: garlicName})
	if err != nil {
		t.Fatal(err)
	}
	both, err := match.Match(context.Background(), fake(), match.Candidate{Name: garlicName, RA: 359.8, Dec: 62.44, HasCoords: true})
	if err != nil {
		t.Fatal(err)
	}
	if top(t, both).Confidence <= top(t, byName).Confidence {
		t.Fatalf("coords did not raise confidence: %.2f vs %.2f", top(t, both).Confidence, top(t, byName).Confidence)
	}
}

func TestMatchRejectsFuzzyWithoutCoordinates(t *testing.T) {
	t.Parallel()
	results, err := match.Match(context.Background(), fake(), match.Candidate{Name: centarusA})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("fuzzy match accepted without coordinates: %v", results)
	}
	results, err = match.Match(context.Background(), fake(), match.Candidate{Name: centarusA, RA: 10.7, Dec: 41.3, HasCoords: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Object.Designation == ngc5128 {
			t.Fatalf("fuzzy match accepted with disagreeing coordinates: %v", r)
		}
	}
}

func TestMatchNameFarFromCoordinates(t *testing.T) {
	t.Parallel()
	results, err := match.Match(context.Background(), fake(), match.Candidate{Name: m31, RA: 359.8, Dec: 62.44, HasCoords: true})
	if err != nil {
		t.Fatal(err)
	}
	var named match.Result
	for _, r := range results {
		if r.Object.Designation == m31 {
			named = r
		}
	}
	if named.Confidence == 0 || named.Confidence > 0.5 || !hasEvidence(named, "from the given coordinates") {
		t.Fatalf("far name match not penalised: %+v", named)
	}
	if top(t, results).Object.Designation != garlicID {
		t.Fatalf("coordinates did not win: %v", top(t, results))
	}
}

func TestMatchComposite(t *testing.T) {
	t.Parallel()
	results, err := match.Match(context.Background(), fake(), match.Candidate{Name: "Omega & Eagle Nebulae"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]match.Result{}
	for _, r := range results {
		got[r.Object.Designation] = r
	}
	for _, want := range []string{"M 16", "M 17"} {
		r, ok := got[want]
		if !ok {
			t.Fatalf("%s missing from %v", want, results)
		}
		if !hasEvidence(r, "names more than one object") {
			t.Fatalf("%s evidence %v", want, r.Evidence)
		}
	}
}

func TestMatchNotCatalogue(t *testing.T) {
	t.Parallel()
	for _, name := range []string{comet, "C/2025 R2 (SWAN)", "Alpha Centauri", "Sirius", "HD 12345"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			results, err := match.Match(context.Background(), fake(), match.Candidate{Name: name, RA: 219.9, Dec: -60.8, HasCoords: true})
			if len(results) != 0 {
				t.Fatalf("%q matched %v", name, results)
			}
			if !errors.Is(err, match.ErrNotCatalogue) {
				t.Fatalf("%q: err %v", name, err)
			}
			var nc *match.NotCatalogueError
			if !errors.As(err, &nc) || nc.Reason == "" {
				t.Fatalf("%q: no reason in %v", name, err)
			}
		})
	}
}

func TestCandidateSubject(t *testing.T) {
	t.Parallel()
	c := match.Candidate{Name: garlicName, Kind: match.KindProject}
	if c.Subject() != "project:Garlic Nebula" {
		t.Fatalf("subject %q", c.Subject())
	}
	if match.Subject(match.KindStack, m31) != "stack:M 31" {
		t.Fatal("stack subject")
	}
}
