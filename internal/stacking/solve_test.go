package stacking

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

// Telescope.live's FOCALLEN is 0; the solver gets the optics from the
// plate solution's pixel scale instead: 4.77"/px with 9 µm pixels is about
// 389 mm.
func TestSolutionOptics(t *testing.T) {
	t.Parallel()
	cards := []frameheader.Card{
		{Name: "XPIXSZ", Value: "9"}, {Name: "FOCALLEN", Value: "0"},
		{Name: "CTYPE1", Value: "RA---TAN-SIP", Quoted: true}, {Name: "CTYPE2", Value: "DEC--TAN-SIP", Quoted: true},
		{Name: "CD1_1", Value: "-3.03066414149E-05"}, {Name: "CD1_2", Value: "0.0013240730461"},
		{Name: "CD2_1", Value: "-0.00132017700099"}, {Name: "CD2_2", Value: "-3.46093717871E-05"},
	}
	focal, pixel := solutionOptics(cardKeywords(cards), cardFloat(cards, "XPIXSZ"))
	if math.Abs(focal-389.8) > 1 || pixel != 9 {
		t.Errorf("optics %.1f mm, %.1f µm; want about 389.8 mm, 9 µm", focal, pixel)
	}
	if focal, pixel := solutionOptics(cardKeywords(cards[:2]), 9); focal != 0 || pixel != 0 {
		t.Errorf("optics %v %v without a solution, want none", focal, pixel)
	}
}
