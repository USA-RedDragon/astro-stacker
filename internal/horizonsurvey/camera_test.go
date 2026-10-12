package horizonsurvey_test

import (
	"math"
	"testing"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
)

func TestWCSMatchesMountGeometry(t *testing.T) {
	t.Parallel()
	s := testSite()
	when := nightTime()
	for _, c := range []struct {
		az, alt, theta float64
		parity         int
		pier           string
	}{
		{100, 5, 23, 1, hs.PierWest},
		{250, 12, 140, -1, hs.PierEast},
		{2, 3, 300, 1, hs.PierEast},
	} {
		ha, dec := s.AltAzToHADec(c.az, c.alt)
		g := hs.NewMountGeometry(1920, 1080, s.Lat, ha, dec, c.theta, c.parity, 6.9)
		wg := hs.NewWCSGeometry(1920, 1080, s, hs.WCSFromGeometry(g, s, when), when)
		for _, p := range [][2]float64{{0, 0}, {1919, 0}, {0, 1079}, {1919, 1079}, {960, 540}} {
			if sep := hs.Separation(g.Dir(p[0], p[1]), wg.Dir(p[0], p[1])); sep > 2e-3 {
				t.Fatalf("pixel %v differs by %.4f deg between mount and WCS geometry", p, sep)
			}
		}
		theta, parity, scale := wg.Orientation()
		if math.Abs(math.Mod(theta-c.theta+540, 360)-180) > 0.05 || parity != c.parity || math.Abs(scale-6.9) > 0.01 {
			t.Fatalf("orientation came back as theta %v parity %d scale %v, want %v %d 6.9", theta, parity, scale, c.theta, c.parity)
		}
		cal := hs.Calibration{Theta: c.theta, PierSide: c.pier, Parity: c.parity, Scale: 6.9}
		other := hs.PierEast
		if c.pier == hs.PierEast {
			other = hs.PierWest
		}
		if th, ok := cal.ThetaFor(other); !ok || math.Abs(math.Mod(th-c.theta+540, 360)-180) != 180 {
			t.Fatalf("other pier side theta %v", th)
		}
		if _, ok := cal.ThetaFor(hs.PierUnknown); ok {
			t.Fatal("unknown pier side must not map")
		}
	}
}

func TestParseWCSFromCDELT(t *testing.T) {
	t.Parallel()
	w, ok := hs.ParseWCS(map[string]string{"CRPIX1": "10", "CRPIX2": "20", "CRVAL1": "30", "CRVAL2": "40", "CDELT1": "-0.001", "CDELT2": "0.001", "CROTA2": "0"})
	if !ok || w.CD[0][0] != -0.001 || w.CD[1][1] != 0.001 {
		t.Fatalf("CDELT WCS parsed as %+v %v", w, ok)
	}
	if _, ok := hs.ParseWCS(map[string]string{"CRPIX1": "10"}); ok {
		t.Fatal("an incomplete WCS must not parse")
	}
}
