package bodies_test

import (
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
)

func TestOwnBody(t *testing.T) {
	t.Parallel()
	stored := map[string]bodies.Target{
		"6 Hebe":          {Designation: "4", Kind: bodies.KindAsteroid},
		"161P with M 57":  {Designation: "event:161p-m57", Kind: bodies.KindEvent},
		"Encke the comet": {Designation: "2P", Kind: bodies.KindComet},
	}
	for name, want := range map[string]string{
		"6 Hebe":                  bodies.KindAsteroid,
		"Encke the comet":         bodies.KindComet,
		"161P/Hartley-IRAS":       bodies.KindComet,
		"C/2025 R2 (SWAN)":        bodies.KindComet,
		"2P":                      bodies.KindComet,
		"161P with M 57":          "",
		"12P Pons-Brooks":         bodies.KindComet,
		"161P with Cr 399 (2026)": "",
		"M 31":                    "",
	} {
		b, ok := bodies.OwnBody(stored, name)
		if ok != (want != "") || (ok && b.Kind != want) {
			t.Errorf("%s: %+v %v, want %q", name, b, ok, want)
		}
	}
}
