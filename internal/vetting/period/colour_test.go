package period

import (
	"math"
	"testing"
)

// TestHottestPrimary3635 is source 3635's colour: Gaia BP-RP 0.997 and
// GSP-Phot E(BP-RP) at most 0.211 (84th percentile). Bayestar19 gives
// E(B-V) under 0.34 out to three sigma beyond the parallax distance, so
// the primary is no hotter than about 6800 K; an 8100 K A5V needs
// E(BP-RP) 0.80. The bound must fall between the two, and a star without
// GSP-Phot reddening gets none.
func TestHottestPrimary3635(t *testing.T) {
	t.Parallel()
	got := hottestPrimary(&Colour{BPRP: 15.794554 - 14.797877, EBPRPUpper: 0.2113})
	if got < 6800 || got > 7300 {
		t.Errorf("hottest primary %.0f K, want 6800 to 7300", got)
	}
	if got := hottestPrimary(nil); got != 0 {
		t.Errorf("no colour gives %.0f K", got)
	}
	if got := hottestPrimary(&Colour{BPRP: 0.2, EBPRPUpper: 0.1}); got != 0 {
		t.Errorf("a blue star gets a bound of %.0f K", got)
	}
}

// TestDarkestCompanionColour is the dark companion bound with and
// without a colour bound: capping the primary at 7000 K can only make the
// darkest companion brighter, and a cap at the hottest table star
// changes nothing.
func TestDarkestCompanionColour(t *testing.T) {
	t.Parallel()
	for _, nm := range []float64{480, 680, 800, 3400} {
		free := darkestCompanion(0.335, nm, 1.22, 0)
		capped := darkestCompanion(0.335, nm, 1.22, 7000)
		if capped < free {
			t.Errorf("%.0f nm: capped %.4f darker than free %.4f", nm, capped, free)
		}
		if same := darkestCompanion(0.335, nm, 1.22, mainSequence[0].teff); math.Abs(same-free) > 1e-12 {
			t.Errorf("%.0f nm: cap at A0V gives %.4f, free %.4f", nm, same, free)
		}
	}
}
