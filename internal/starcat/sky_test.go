package starcat_test

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
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

func TestDecimalMatchesVizieR(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v    float32
		n    int
		want string
	}{{17.49252, 6, "17.492520"}, {1.4605, 3, "1.460"}, {17.16435, 4, "17.1643"}, {4.76655, 4, "4.7665"}, {17.1015625, 6, "17.101563"}, {-17.1015625, 6, "-17.101563"}, {20.68319, 6, "20.683190"}, {0.0001234, 7, "0.0001234"}} {
		if got := strconv.FormatFloat(starcat.Decimal(c.v), 'f', c.n, 64); got != c.want {
			t.Errorf("Decimal(%v) to %d places = %s, want %s", c.v, c.n, got, c.want)
		}
	}
}
