package vetting

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

// TestPickStar is failure mode A5: difference image centroids of blended
// stars sit a pixel or more off the star, so the spots of 17073 (2.52
// arcsec from a G 14.73 star), 49921 (2.12 arcsec from G 15.01) and 48806
// (3.0 arcsec from G 14.06) found no star within 2 arcsec, and the vet
// read the wrong light curves. The nearest star up to 4 arcsec off is
// taken when its G agrees with the spot's reference magnitude within 1.5;
// 16040's own star must win over a slightly brighter one 3 arcsec away.
func TestPickStar(t *testing.T) {
	t.Parallel()
	const ra, dec = 312.9, 29.2
	at := func(sep, g float64, id int64) starcat.GaiaStar {
		return starcat.GaiaStar{SourceID: id, RA: ra, Dec: dec + sep/3600, G: float32(g), PMRA: 0, PMDec: 0}
	}
	for _, c := range []struct {
		name   string
		stars  []starcat.GaiaStar
		refMag float64
		want   int64
	}{
		{"17073", []starcat.GaiaStar{at(2.52, 14.73, 1)}, 15.0, 1},
		{"49921", []starcat.GaiaStar{at(1.1, 20.4, 2), at(2.12, 15.01, 1)}, 15.2, 1},
		{"48806", []starcat.GaiaStar{at(3.0, 14.06, 1)}, 14.7, 1},
		{"16040 keeps its star", []starcat.GaiaStar{at(0.31, 17.08, 1), at(1.18, 19.55, 3), at(3.01, 16.98, 2)}, 16.5, 1},
		{"3095 keeps its star", []starcat.GaiaStar{at(0.17, 17.93, 1), at(2.68, 17.58, 2)}, 17.9, 1},
		{"too faint for the spot", []starcat.GaiaStar{at(3.5, 18.2, 1)}, 15.0, 0},
		{"beyond 4 arcsec", []starcat.GaiaStar{at(4.5, 15.0, 1)}, 15.0, 0},
		{"no reference magnitude keeps 2 arcsec", []starcat.GaiaStar{at(1.1, 20.4, 2), at(2.52, 14.73, 1)}, math.NaN(), 2},
		{"close star, magnitude off", []starcat.GaiaStar{at(0.5, 19.0, 2)}, 15.0, 2},
	} {
		got := int64(0)
		if i := pickStar(c.stars, ra, dec, starcat.GaiaEpoch, c.refMag); i >= 0 {
			got = c.stars[i].SourceID
		}
		if got != c.want {
			t.Errorf("%s: picked %d, want %d", c.name, got, c.want)
		}
	}
}
