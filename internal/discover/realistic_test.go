package discover_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
)

const (
	heartAndSoul = "Heart and Soul"
	markarian    = "Markarian Chain"
)

func catalogService(t *testing.T) *discover.Service {
	t.Helper()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	return &discover.Service{Catalog: ix}
}

type realSubject struct {
	name    string
	targets []string
	at      string
	ra, dec float64
	radius  float64
	object  bool
}

func panels(name string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s Panel %d", name, i+1)
	}
	return out
}

func realisticSubjects() []realSubject {
	return []realSubject{
		{name: garlicName, ra: 359.804, dec: 62.437},
		{name: "Cygnis Loop", targets: panels("Cygnis Loop", 2), ra: 313.02, dec: 30.65, radius: 0.89},
		{name: "M13", targets: []string{m13Name}, at: m13Name},
		{name: "Rho", targets: panels("IC 4604", 12), at: "IC 4604", radius: 1.5},
		{name: "Sadr Region", targets: panels("IC 1318", 15), at: "IC 1318", radius: 2},
		{name: markarian, targets: panels(markarian, 9), ra: 186.75, dec: 13.1, radius: 1},
		{name: "Taurus Dark Cloud and co", targets: panels("Taurus Dark Cloud and co", 6), ra: 68, dec: 25.5, radius: 1.5},
		{name: "IC 405", targets: panels("IC 405", 4), at: "IC 405", radius: 0.8},
		{name: "NGC 2264", targets: panels("NGC 2264", 4), at: "NGC 2264", radius: 0.8},
		{name: "Pelican", targets: panels("Pelican", 4), at: "IC 5070", radius: 0.8},
		{name: "Tadpole", targets: panels("IC 410", 4), at: "IC 410", radius: 0.8},
		{name: "Galaxy Cluster", targets: panels("IC 3393", 4), at: "IC 3393", radius: 0.8},
		{name: heartAndSoul, targets: panels(heartAndSoul, 3), ra: 40.5, dec: 61.0, radius: 1.2},
		{name: "Heart and Soul SHO", targets: panels("Heart and Soul SHO", 3), ra: 40.5, dec: 61.0, radius: 1.2},
		{name: "Witch Head", targets: panels("NGC 1909", 3), at: "NGC 1909", radius: 0.8},
		{name: "Blue Horsehead", targets: panels("Blue Horsehead", 2), at: "IC 4592", radius: 0.6},
		{name: "California", targets: panels("California", 2), at: "NGC 1499", radius: 0.6},
		{name: "Eagle", targets: panels("Eagle", 2), at: "M 16", radius: 0.6},
		{name: "Elephant Trunk", targets: panels("IC 1396", 2), at: "IC 1396", radius: 0.6},
		{name: "NGC 7822", targets: panels("NGC 7822", 2), at: "NGC 7822", radius: 0.6},
		{name: "North America", targets: panels("North America", 2), at: "NGC 7000", radius: 0.6},
		{name: "Sagittarius Star Cloud", targets: panels("Sagittarius Star Cloud", 2), at: "M 24", radius: 0.6},
		{name: "Spaghetti", targets: panels("Spaghetti", 2), at: "Sh2-240", radius: 0.6},
		{name: "Rosette", targets: []string{"Rosette", "Rosette SHO"}, at: "NGC 2237"},
		{name: "Omega & Eagle Nebulae", ra: 274.95, dec: -15.0, object: true},
		{name: "M 8 and M 20", ra: 270.8, dec: -23.7, object: true},
		{name: "Centarus A", at: "NGC 5128"},
		{name: "Abell 85", ra: 10.46, dec: -9.3},
		{name: "Region around WR102", ra: 266.45, dec: -26.17},
		{name: "Statue of Liberty Nebula", at: "NGC 3576", object: true},
		{name: "NGC1313", at: "NGC 1313", object: true},
		{name: "SH2-129", at: "Sh2-129", object: true},
		{name: "gum 3", at: "Gum 3", object: true},
		{name: markarian, targets: []string{markarian + " Panel 3"}, ra: 186.9, dec: 13.0, object: true},
		{name: leoTriplet, ra: 170.0, dec: 13.3, object: true},
		{name: "C/2025 R2", ra: 200, dec: 10},
		{name: "Alpha Centauri", ra: 219.9, dec: -60.83},
		{name: "Sirius", ra: 101.287, dec: -16.716, object: true},
		{name: "Eta Carinae", ra: 161.265, dec: -59.68, object: true},
		{name: "Andromeda"},
		{name: "Veil", ra: 311.4, dec: 30.71},
	}
}

func buildRealistic(t *testing.T, s *discover.Service) []discover.Subject {
	t.Helper()
	rs := realisticSubjects()
	out := make([]discover.Subject, 0, len(rs))
	for _, r := range rs {
		kind := discover.SubjectProject
		if r.object {
			kind = discover.SubjectObject
		}
		targets := r.targets
		if targets == nil {
			targets = []string{r.name}
		}
		subj := discover.Subject{Key: kind + ":" + r.name, Name: r.name, Kind: kind, Targets: targets, Radius: r.radius,
			Mosaic: len(targets) > 1, Hours: map[string]float64{}}
		switch {
		case r.at != "":
			o, ok := s.Catalog.Lookup(r.at)
			if !ok {
				t.Fatalf("%s not in the catalogue", r.at)
			}
			subj.RA, subj.Dec, subj.HasPos = o.RA, o.Dec, true
		case r.ra != 0 || r.dec != 0:
			subj.RA, subj.Dec, subj.HasPos = r.ra, r.dec, true
		}
		out = append(out, subj)
	}
	return out
}

type realResult struct {
	links  []discover.Link
	reason string
}

func matchRealistic(t *testing.T) map[string]realResult {
	t.Helper()
	s := catalogService(t)
	out := map[string]realResult{}
	for _, subj := range buildRealistic(t, s) {
		links, reason := s.MatchSubject(context.Background(), subj)
		out[subj.Key] = realResult{links, reason}
	}
	return out
}

func TestRealisticNotCatalogue(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"project:C/2025 R2":      catalog.ReasonComet,
		"project:Alpha Centauri": catalog.ReasonBayer,
		"object:Sirius":          catalog.ReasonStar,
	}
	for key, r := range matchRealistic(t) {
		if r.reason != want[key] {
			t.Errorf("%s: not-catalogue reason %q, want %q", key, r.reason, want[key])
		}
		if r.reason == "" {
			continue
		}
		for _, l := range r.links {
			if l.Status != discover.StatusInFrame {
				t.Errorf("%s is not a catalogue object but links %s as %s", key, l.Object.Designation, l.Status)
			}
		}
	}
}

func TestNotCatalogueInACrowdedField(t *testing.T) {
	t.Parallel()
	s := catalogService(t)
	o, _ := s.Catalog.Lookup("IC 1318")
	at := func(name string) discover.Subject {
		return discover.Subject{Key: "project:" + name, Name: name, Kind: discover.SubjectProject, Targets: []string{name},
			RA: o.RA, Dec: o.Dec, HasPos: true, Hours: map[string]float64{}}
	}
	links, reason := s.MatchSubject(context.Background(), at("HD 194093"))
	if reason != catalog.ReasonStarID {
		t.Errorf("HD number reason %q", reason)
	}
	for _, l := range links {
		if l.Status != discover.StatusInFrame {
			t.Errorf("HD number links %s as %s", l.Object.Designation, l.Status)
		}
	}
	links, reason = s.MatchSubject(context.Background(), at("Gamma Cygni"))
	if reason != "" || len(links) == 0 {
		t.Errorf("a star name with objects at its position was dropped: %q %+v", reason, links)
	}
}
