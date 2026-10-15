package lc_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

func TestBJDAgainstZTF(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ra, dec, mjd, exptime, hjd float64
	}{
		{323.277032, 58.634305, 59040.4815162, 30, 2459040.9821616486},
		{323.277032, 58.634305, 59130.314838, 60, 2459130.817590266},
		{316.161804, 43.320665, 58999.3892593, 60, 2458999.8895499296},
		{10.889321, 41.662433, 58274.4511574, 30, 2458274.9479573667},
	}
	for _, c := range cases {
		b := lc.BJD(lc.MJDTime(c.mjd+c.exptime/2/86400), c.ra, c.dec)
		if d := (b-c.hjd)*86400 - 69.184; math.Abs(d) > 5 {
			t.Errorf("mjd %.5f: BJD minus HJD is %.2f s off the 69.184 s clock offset", c.mjd, d)
		}
	}
}

func TestBJDLeapSeconds(t *testing.T) {
	t.Parallel()
	before := lc.BJD(lc.MJDTime(57203.99), 0, 90)
	after := lc.BJD(lc.MJDTime(57204.01), 0, 90)
	if d := (after-before)*86400 - 0.02*86400; math.Abs(d-1) > 0.2 {
		t.Errorf("2015-07-01 leap second: got %.3f s, want 1 s", d)
	}
}
