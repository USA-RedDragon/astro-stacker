package catalog_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

const (
	m31     = "M 31"
	c14     = "C 14"
	abell85 = "Abell 85"
	garlic  = "G116.9+00.2"
	messier = "messier"
	sh216   = "Sh2-216"
)

func TestCanonical(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"M31": m31, "m 031": m31, "Messier 42": "M 42", "NGC1313": "NGC 1313", "ngc 0281": "NGC 281",
		"IC 1318B": "IC 1318B", "SH2-129": "Sh2-129", "Sh 2-155": "Sh2-155", "sharpless 101": "Sh2-101",
		"gum 3": "Gum 3", "B33": "B 33", "Barnard 150": "B 150", "vdB 1": "vdB 1", "C 014": c14, "Caldwell 49": "C 49",
		"LDN1235": "LDN 1235", abell85: abell85, "HCG 92": "HCG 92", "Arp 273": "Arp 273",
		"G116.9+0.2": garlic, "SNR G74.0-8.5": "G074.0-08.5", "Mel 22": "Mel 22", "Cr 399": "Cr 399",
	}
	for in, want := range cases {
		got, ok := catalog.Canonical(in)
		if !ok || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"Garlic Nebula", "M 1-92", "M 111", "C 110", "Sh2-400", ""} {
		if got, ok := catalog.Canonical(in); ok {
			t.Errorf("Canonical(%q) = %q, want no designation", in, got)
		}
	}
}

func TestKeyAndNames(t *testing.T) {
	t.Parallel()
	if catalog.Key("ngc 7000") != catalog.Key("NGC7000") {
		t.Error("keys differ for the same designation")
	}
	if got := catalog.StripPanel("IC 4604 Panel 12"); got != "IC 4604" {
		t.Errorf("StripPanel = %q", got)
	}
	if got := catalog.StripPanel("Cygnis Loop P2"); got != "Cygnis Loop" {
		t.Errorf("StripPanel = %q", got)
	}
	if got := catalog.CoreName("Heart and Soul Nebula SHO Panel 3"); got != "heart soul" {
		t.Errorf("CoreName = %q", got)
	}
	if got := catalog.NormalizeName("Barnard's E"); got != "barnards e" {
		t.Errorf("NormalizeName = %q", got)
	}
	if s := catalog.Similarity("cygnis loop", "cygnus loop"); s < 0.5 {
		t.Errorf("Similarity of a one-letter typo = %.2f", s)
	}
	if s := catalog.Similarity("garlic", "rosette"); s > 0.2 {
		t.Errorf("Similarity of unrelated names = %.2f", s)
	}
}

func TestSharplessSpellings(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"Sharpless 216": sh216, "Sharpless 2-216": sh216, "Sh2 216": sh216, "SH 2-16": "Sh2-16"} {
		if got, ok := catalog.Canonical(in); !ok || got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}
