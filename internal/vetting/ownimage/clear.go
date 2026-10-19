package ownimage

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	// clearEdgePx is how far from the covered area's edge a spot must be
	// for its own frames alone to be trusted: spots 45 to 129 px from it
	// on Markarian Chain Panel 8, Caldwell 14 and Sombrero Galaxy were
	// background steps and gradient bands, at independent signal to noise
	// 0.4 to 2.6.
	clearEdgePx = 150.0
	// haloArcsec, haloV and haloSlope set how far a bright star's halo
	// and spikes reach: haloArcsec at V haloV, growing haloSlope dex per
	// magnitude brighter, 6.6' at V 1.7. Eight spots 2.4' to 6.2' from
	// Alnitak and Alnilam on Horsehead Nebula measured 3 to 5 times the
	// noise the frames gave.
	haloArcsec = 60.0
	haloV      = 8.0
	haloSlope  = 0.13
	// ringArcsec is how far the reflection ring of a star of V ringV or
	// brighter reaches. Starless spots on Horsehead Nebula stood 0.9 per
	// arcmin of radius 6' to 7' from Alnilam (V 1.69), 2.0 to 3.2 from 7'
	// to 10' and 0.2 to 0.9 from 10' to 20': 33725 and 34238 sat 8.1' and
	// 8.7' from it.
	ringArcsec = 600.0
	ringV      = 2.5
)

// Halo is the bright star whose halo reaches the spot.
type Halo struct {
	V         float64 `json:"v"`
	SepArcsec float64 `json:"sep_arcsec"`
	Reach     float64 `json:"reach_arcsec"`
}

// haloReach is how far, in arcsec, a star of magnitude v spreads light.
func haloReach(v float64) float64 {
	r := haloArcsec * math.Pow(10, haloSlope*(haloV-v))
	if v <= ringV {
		r = math.Max(r, ringArcsec)
	}
	return r
}

// haloAt finds the bright star whose halo reaches farthest past the spot,
// from Gaia's stars around it and the brightest Hipparcos stars.
func (f *field) haloAt(sp *spotPlan) *Halo {
	var best *Halo
	consider := func(ra, dec, v float64) {
		if v >= haloV {
			return
		}
		sep := discoveries.Separation(ra, dec, sp.spot.RA, sp.spot.Dec) * 3600
		reach := haloReach(v)
		if sep > reach || (best != nil && sep/reach >= best.SepArcsec/best.Reach) {
			return
		}
		best = &Halo{V: v, SepArcsec: sep, Reach: reach}
	}
	for _, s := range f.pl {
		consider(s.RA, s.Dec, s.G)
	}
	for _, s := range hipparcosBright() {
		consider(float64(s[0]), float64(s[1]), float64(s[2]))
	}
	return best
}

// edgeDistance is how far the spot is from the nearest pixel a master
// does not cover, or the frame's border, up to clearEdgePx.
func edgeDistance(p *discoveries.Plane, x, y float64) float64 {
	best := clearEdgePx
	r := int(clearEdgePx)
	cx, cy := int(math.Round(x)), int(math.Round(y))
	for yy := cy - r; yy <= cy+r; yy++ {
		for xx := cx - r; xx <= cx+r; xx++ {
			d := math.Hypot(float64(xx)-x, float64(yy)-y)
			if d >= best {
				continue
			}
			if xx < 0 || yy < 0 || xx >= p.W || yy >= p.H || isNaN32(p.At(xx, yy)) {
				best = d
			}
		}
	}
	return best
}
