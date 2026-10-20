package vetting

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/surveys"
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

// TestMatchNote is failure mode N2: 28575 (Abell 85) and 30023 (Cygnus
// Loop Panel 2) listed the G of the Gaia star nearest the spot, 18.11 and
// 19.57, while the vet read the ZTF light of the star whose G fits the
// spot's reference light, 15.17 and 16.07, without saying so, which read
// as a light curve matched to the wrong star.
func TestMatchNote(t *testing.T) {
	t.Parallel()
	const ra, dec = 3.0869, 63.3977
	at := func(sep, g float64, id int64) starcat.GaiaStar {
		return starcat.GaiaStar{SourceID: id, RA: ra, Dec: dec + sep/3600, G: float32(g)}
	}
	for _, c := range []struct {
		name   string
		stars  []starcat.GaiaStar
		refMag float64
		want   string
	}{
		{"28575", []starcat.GaiaStar{at(0.82, 18.11, 2), at(2.06, 19.54, 3), at(3.47, 15.17, 1)}, 15.62,
			"The spot's reference light of magnitude 15.6 is that of the G 15.17 star 3.5\" from it, not of the nearer G 18.11 star 0.8\" away, so the checks read the G 15.17 star."},
		{"30023", []starcat.GaiaStar{at(2.12, 19.57, 2), at(2.34, 16.07, 1)}, 16.44,
			"The spot's reference light of magnitude 16.4 is that of the G 16.07 star 2.3\" from it, not of the nearer G 19.57 star 2.1\" away, so the checks read the G 16.07 star."},
		{"nearest star taken", []starcat.GaiaStar{at(0.42, 15.50, 1), at(1.64, 16.35, 2)}, 15.4, ""},
		{"no reference light", []starcat.GaiaStar{at(1.1, 20.4, 2), at(1.5, 15.0, 1)}, math.NaN(), ""},
	} {
		best := pickStar(c.stars, ra, dec, starcat.GaiaEpoch, c.refMag)
		if best < 0 {
			t.Fatalf("%s: no star", c.name)
		}
		st := star{found: true, target: surveys.Target{G: float64(c.stars[best].G)}}
		st.skipped, st.sep = skippedOf(c.stars, best, ra, dec, starcat.GaiaEpoch)
		if got := st.matchNote(c.refMag); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
