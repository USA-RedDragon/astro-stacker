package starcat_test

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/starcat"
)

func TestCoverHoldsEveryPointInTheCone(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	for range 300 {
		ra := rng.Float64() * 360
		dec := math.Asin(2*rng.Float64()-1) * 180 / math.Pi
		if rng.IntN(10) == 0 {
			dec = math.Copysign(89.9+0.1*rng.Float64(), dec)
		}
		r := 0.01 + rng.Float64()*3
		for _, order := range []int{3, 6} {
			cover := starcat.Cover(order, ra, dec, r)
			for range 200 {
				a := ra + (rng.Float64()*2-1)*r*4
				d := math.Max(-90, math.Min(90, dec+(rng.Float64()*2-1)*r))
				a = math.Mod(a+360, 360)
				if starcat.Separation(ra, dec, a, d) > r {
					continue
				}
				if p := starcat.Pixel(order, a, d); !slices.Contains(cover, p) {
					t.Fatalf("order %d cone (%v, %v, %v): point (%v, %v) in pixel %d is not covered", order, ra, dec, r, a, d, p)
				}
			}
		}
	}
}

func TestPropagateMovesByProperMotion(t *testing.T) {
	t.Parallel()
	ra, dec := starcat.Propagate(269.44988505182, 4.73985735848, 1.306, -0.842, -16)
	if math.Abs(ra-269.44987922578) > 5e-9 || math.Abs(dec-4.73986109973) > 5e-9 {
		t.Fatalf("Gaia DR3 4472832818126065408 at J2000 = (%.11f, %.11f), VizieR gives (269.44987922578, 4.73986109973)", ra, dec)
	}
	ra, dec = starcat.Propagate(269.44850252544, 4.73942005111, -801.551, 10362.394, -16)
	if starcat.Separation(ra, dec, 269.45207342819, 4.69341045776) > 0.25/3600 {
		t.Fatalf("Barnard's star at J2000 = (%.8f, %.8f), VizieR gives (269.45207343, 4.69341046) with its radial velocity", ra, dec)
	}
	if a, d := starcat.Propagate(10, 20, math.NaN(), math.NaN(), 10); a != 10 || d != 20 {
		t.Fatalf("no proper motion moved the star to (%v, %v)", a, d)
	}
}
