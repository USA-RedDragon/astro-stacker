package period

import "github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"

// SameDepthOddEven starts the double check's evidence when a single dip
// does not differ between odd and even cycles. Two equal eclipses at
// twice the period look the same, so it does not rule that period out.
const SameDepthOddEven = "Odd and even cycles do not rule out two equal dips"

// OldSameDepthOddEven is how reports before star:16 started the same
// evidence.
const OldSameDepthOddEven = "The dip has the same depth on odd and even cycles"

// Shape is the fitted dip profile at u cycles from its minimum, 1 at the
// minimum and 0 outside the half width w; a is the shape exponent.
func Shape(u, w, a float64) float64 { return shapeOnly(u, w, a) }

// WrapPhase folds x cycles into [-0.5, 0.5).
func WrapPhase(x float64) float64 { return wrapPhase(x) }

// BandKey is the key a series' depths are stored under, "ZTF r".
func BandKey(survey, band string) string {
	if survey == lc.TESS || band == "" {
		return survey
	}
	return survey + " " + band
}
