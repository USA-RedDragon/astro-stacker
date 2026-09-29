package frameheader_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

func TestNormalizeFilter(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Halpha": "H-a", "H-alpha": "H-a", "H-a": "H-a", "OIII": "O-III", "Oiii": "O-III",
		"O-III": "O-III", "SII": "S-II", "Sii": "S-II", "Red": "Red", "Luminance": "Luminance", "": "",
	} {
		if got := frameheader.NormalizeFilter(in); got != want {
			t.Errorf("NormalizeFilter(%q) = %q, want %q", in, got, want)
		}
	}
}
