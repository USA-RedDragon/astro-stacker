// Package discoverysource gathers the discovery candidates of one spot into
// one source across nights and filter groups, keeps its vetting, and lists
// the sources for the Discoveries inbox.
package discoverysource

import (
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryalert"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

// GaiaStarArcsec is how far apart two changes of a target may be and still
// be one source when both were matched to the same Gaia star. Detection
// matches a change to a star within 1.5 px of where the star's proper motion
// puts it on the night, so two changes on one star are at most 3 px apart:
// 5.8″ at the 1.92″ pixels of the rig.
const GaiaStarArcsec = 6.0

// gaiaSameMag is how close two Gaia G magnitudes must be to be one star's;
// the stored G is the catalog value, so one star's is identical.
const gaiaSameMag = 1e-4

// rank orders the classes by how much they matter: a possible nova or new
// variable over anything known, and low confidence last.
func rank(class string) int {
	order := []string{
		discoveries.ClassNova, discoveries.ClassSupernova, discoveries.ClassNewVariable, discoveries.ClassUnknownMover,
		discoveries.ClassKnownTransient, discoveries.ClassKnownVariable, discoveries.ClassKnownAsteroid, discoveries.ClassLowConfidence,
	}
	if i := slices.Index(order, class); i >= 0 {
		return len(order) - i
	}
	return 0
}

// Better says whether class a matters more than class b.
func Better(a, b string) bool { return rank(a) > rank(b) }

func anchorOf(s app.DiscoverySource) app.DiscoveryAlert {
	return app.DiscoveryAlert{Kind: s.Kind, Object: s.Object, RA: s.RA, Dec: s.Dec, Night: s.Night, At: s.At, RateArcmin: s.RateArcmin, PA: s.PA}
}

func alertOf(c app.DiscoveryCandidate) app.DiscoveryAlert {
	a := app.DiscoveryAlert{Kind: c.Kind, Object: c.Object, RA: c.RA, Dec: c.Dec, Night: c.Night}
	if m, ok := discoveryalert.MotionOf(c); ok {
		at, rate, pa := m.At, m.RateArcmin, m.PA
		a.At, a.RateArcmin, a.PA = &at, &rate, &pa
	}
	return a
}

func sameStar(object string, ra, dec float64, g *float64, c app.DiscoveryCandidate) bool {
	if g == nil || c.GaiaG == nil || object != c.Object || c.Kind != app.DiscoveryKindChange {
		return false
	}
	return math.Abs(*g-*c.GaiaG) < gaiaSameMag && discoveryalert.SepArcsec(ra, dec, c.RA, c.Dec) <= GaiaStarArcsec
}

// SameCandidate says whether two candidates are one source: the alerts'
// test of one source (a change at the same place of the same target, a
// mover on the other's path), or two changes of a target on the same Gaia
// star.
func SameCandidate(a, b app.DiscoveryCandidate) bool {
	if a.Kind == app.DiscoveryKindChange && sameStar(a.Object, a.RA, a.Dec, a.GaiaG, b) {
		return true
	}
	return discoveryalert.Same(alertOf(a), b)
}

// Matches says whether a candidate belongs to a source, given the source's
// candidates already stored.
func Matches(s app.DiscoverySource, members []app.DiscoveryCandidate, c app.DiscoveryCandidate) bool {
	if s.Kind != c.Kind || s.Object != c.Object {
		return false
	}
	if discoveryalert.Same(anchorOf(s), c) {
		return true
	}
	if c.Kind != app.DiscoveryKindChange {
		return false
	}
	if sameStar(s.Object, s.RA, s.Dec, s.GaiaG, c) {
		return true
	}
	return slices.ContainsFunc(members, func(m app.DiscoveryCandidate) bool { return SameCandidate(m, c) })
}
