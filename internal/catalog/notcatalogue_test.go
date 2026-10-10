package catalog_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

func TestNotCatalogue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		reason  string
		certain bool
	}{
		{"C/2025 R2", catalog.ReasonComet, true},
		{"C/2025 R2 (SWAN)", catalog.ReasonComet, true},
		{"12P/Pons-Brooks", catalog.ReasonComet, true},
		{"P/2010 A2", catalog.ReasonComet, true},
		{"Comet Lemmon", catalog.ReasonComet, true},
		{"HD 12345", catalog.ReasonStarID, true},
		{"HIP 71683", catalog.ReasonStarID, true},
		{"SAO 252838", catalog.ReasonStarID, true},
		{"TYC 9007-5848-1", catalog.ReasonStarID, true},
		{"Alpha Centauri", catalog.ReasonBayer, false},
		{"Eta Carinae", catalog.ReasonBayer, false},
		{"Sirius", catalog.ReasonStar, false},
		{"Beta Ursae Minoris", catalog.ReasonBayer, false},
		{"61 Cygni", catalog.ReasonFlamsteed, false},
		{"alp Cen", "", false},
		{m31, "", false},
		{"Cygnus Loop", "", false},
		{"Gamma Cygni Nebula", "", false},
		{"Heart and Soul", "", false},
		{"Rho", "", false},
		{"Omega & Eagle Nebulae", "", false},
		{"Comets Tail Nebula", "", false},
		{"C 49", "", false},
		{"IC 1318 Panel 3", "", false},
		{"Region around WR102", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			reason, certain := catalog.NotCatalogue(tc.in)
			if reason != tc.reason || certain != tc.certain {
				t.Fatalf("NotCatalogue(%q) = %q, %v; want %q, %v", tc.in, reason, certain, tc.reason, tc.certain)
			}
		})
	}
}
