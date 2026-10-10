package match_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog/match"
)

func TestNormalizeDesignation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{m31Spelled, m31, true},
		{"m 31", m31, true},
		{"Messier 031", m31, true},
		{ngc1313Spelled, ngc1313, true},
		{"NGC 0224", "NGC 224", true},
		{"ngc-7000", "NGC 7000", true},
		{ic4604, ic4604, true},
		{sh2129Spelled, sh2129, true},
		{"Sh2 129", sh2129, true},
		{"Sh 2-129", sh2129, true},
		{"Sharpless 129", sh2129, true},
		{"sh2-0103", sh2103, true},
		{"LDN 1235", "LDN 1235", true},
		{"LBN 1111", "LBN 1111", true},
		{b33, b33, true},
		{"Barnard 33", b33, true},
		{vdb16, vdb16, true},
		{"VDB016", vdb16, true},
		{abell426, abell426, true},
		{"ACO 426", abell426, true},
		{"Arp 273", "Arp 273", true},
		{hcg92, hcg92, true},
		{"Hickson 92", hcg92, true},
		{cr399, cr399, true},
		{"Collinder 399", cr399, true},
		{"Col 399", cr399, true},
		{mel111, mel111, true},
		{"Melotte 111", mel111, true},
		{"gum 3", gum3, true},
		{"RCW 47", "RCW 47", true},
		{"Ced 211", "Ced 211", true},
		{"PGC 2557", "PGC 2557", true},
		{c34, c34, true},
		{"Caldwell 34", c34, true},
		{"PK 130-11.1", "PK 130-11.1", true},
		{"PK 080-06.1", "PK 80-6.1", true},
		{"UGC 62", "UGC 62", true},
		{"M 1-92", "", false},
		{andromeda, "", false},
		{comet, "", false},
		{"M 200", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, ok := match.NormalizeDesignation(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("NormalizeDesignation(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDesignationKey(t *testing.T) {
	t.Parallel()
	if match.DesignationKey(sh2129Spelled) != match.DesignationKey("Sh 2 129") {
		t.Fatal("Sh2 spellings have different keys")
	}
	if match.DesignationKey("ACO 426") != match.DesignationKey("Abell 0426") {
		t.Fatal("Abell spellings have different keys")
	}
	if match.DesignationKey("Berkeley 104") != match.DesignationKey("berkeley  104") {
		t.Fatal("unrecognised catalogues are not keyed by spacing and case")
	}
}

func TestStripPanel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		base  string
		panel int
	}{
		{ic4604Panel6, ic4604, 6},
		{"Markarian Chain Panel 3", "Markarian Chain", 3},
		{"Cygnis Loop Panel6", cygnisLoop, 6},
		{"Cygnis Loop P2", cygnisLoop, 2},
		{sh2129, sh2129, 0},
		{m31, m31, 0},
		{"  Heart Nebula  ", "Heart Nebula", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			base, panel := match.StripPanel(tc.in)
			if base != tc.base || panel != tc.panel {
				t.Fatalf("StripPanel(%q) = %q, %d; want %q, %d", tc.in, base, panel, tc.base, tc.panel)
			}
		})
	}
}

func TestParseCoordinates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		ra, dec float64
		ok      bool
	}{
		{"20h45m +30.7", 311.25, 30.7, true},
		{"311.3 30.7", 311.3, 30.7, true},
		{"311.3, -30.7", 311.3, -30.7, true},
		{"RA 20:45:38 Dec +30:42", 311.4083, 30.7, true},
		{"00h42m44s +41d16m09s", 10.6833, 41.2692, true},
		{"05h35m17s -05°23′28″", 83.8208, -5.3911, true},
		{"20 45 38 +30 42 00", 311.4083, 30.7, true},
		{"ra=23.987h dec=62.44", 359.805, 62.44, true},
		{ngc1313, 0, 0, false},
		{m31, 0, 0, false},
		{"25h00m +10", 0, 0, false},
		{"400 10", 0, 0, false},
		{"10 95", 0, 0, false},
		{"20h45m +30:75", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			ra, dec, ok := match.ParseCoordinates(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseCoordinates(%q) ok = %v", tc.in, ok)
			}
			if ok && (math.Abs(ra-tc.ra) > 0.001 || math.Abs(dec-tc.dec) > 0.001) {
				t.Fatalf("ParseCoordinates(%q) = %f, %f; want %f, %f", tc.in, ra, dec, tc.ra, tc.dec)
			}
		})
	}
}

func TestNotCatalogue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in string
		ok bool
	}{
		{comet, true},
		{"C/2025 R2 (SWAN)", true},
		{"12P/Pons-Brooks", true},
		{"Comet Lemmon", true},
		{"Alpha Centauri", true},
		{"alp Cen", false},
		{"Sirius", true},
		{"HD 12345", true},
		{"61 Cygni", true},
		{"Eta Carinae", true},
		{m31, false},
		{"Cygnus Loop", false},
		{"Gamma Cygni Nebula", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			reason, ok := match.NotCatalogue(tc.in)
			if ok != tc.ok {
				t.Fatalf("NotCatalogue(%q) = %q, %v", tc.in, reason, ok)
			}
			if ok && reason == "" {
				t.Fatalf("NotCatalogue(%q) gave no reason", tc.in)
			}
		})
	}
}
