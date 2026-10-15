package discoveries

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// NewLight is the magnitude of the light a change added since the
// reference: the brightening as the difference measures it, not the total
// light at the spot, which on a galaxy holds the galaxy's light too.
// Candidate 18554, the M 31 nova AT 2026acsi, had 18.06 at its spot on the
// night against 18.49 before, so 19.26 of new light. A source with nothing
// measured there before is all new light, and a mover's magnitude is its
// own; a fading source added none, and has no new light.
func NewLight(c app.DiscoveryCandidate) *float64 {
	if c.NightMag == nil || math.IsNaN(*c.NightMag) {
		return nil
	}
	if c.Kind == app.DiscoveryKindMover || c.RefMag == nil {
		if c.Sign < 0 {
			return nil
		}
		m := *c.NightMag
		return &m
	}
	f := math.Pow(10, -0.4**c.NightMag) - math.Pow(10, -0.4**c.RefMag)
	if !(f > 0) {
		return nil
	}
	m := -2.5 * math.Log10(f)
	return &m
}
