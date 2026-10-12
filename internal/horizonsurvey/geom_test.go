package horizonsurvey_test

import (
	"math"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestAltAzHADecRoundTrip(t *testing.T) {
	t.Parallel()
	s := testSite()
	for az := 0.0; az < 360; az += 17 {
		for alt := -5.0; alt < 89; alt += 7 {
			ha, dec := s.AltAzToHADec(az, alt)
			az2, alt2 := s.HADecToAltAz(ha, dec)
			if !near(alt, alt2, 1e-9) || !near(math.Mod(az-az2+540, 360)-180, 0, 1e-7) {
				t.Fatalf("az %v alt %v came back as az %v alt %v", az, alt, az2, alt2)
			}
		}
	}
	ha, dec := s.AltAzToHADec(180, 90-s.Lat)
	if !near(ha, 0, 1e-9) || !near(dec, 0, 1e-9) {
		t.Fatalf("south meridian at the equator gave HA %v dec %v", ha, dec)
	}
	ha, dec = s.AltAzToHADec(90, 0)
	if !near(ha, -90, 1e-9) || !near(dec, 0, 1e-9) {
		t.Fatalf("east point gave HA %v dec %v", ha, dec)
	}
	if _, alt := s.HADecToAltAz(0, 90); !near(alt, s.Lat, 1e-9) {
		t.Fatalf("pole altitude %v, want the latitude", alt)
	}
}

func TestPrecession(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	ra, dec := hs.J2000ToDate(0, 0, when)
	years := 26.78
	if !near(ra, 3.0749*15/3600*years, 0.005) || !near(dec, 20.043/3600*years, 0.005) {
		t.Fatalf("J2000 origin precessed to RA %v dec %v", ra, dec)
	}
	for _, p := range [][2]float64{{10, 20}, {200, -40}, {359, 85}} {
		r, d := hs.J2000ToDate(p[0], p[1], when)
		r2, d2 := hs.DateToJ2000(r, d, when)
		if !near(math.Mod(r2-p[0]+540, 360)-180, 0, 1e-8) || !near(d2, p[1], 1e-8) {
			t.Fatalf("round trip of %v gave %v %v", p, r2, d2)
		}
	}
}

func TestSunPosition(t *testing.T) {
	t.Parallel()
	s := testSite()
	az, alt := s.SunAltAz(time.Date(2026, 10, 13, 0, 5, 0, 0, time.UTC))
	if !near(alt, 0, 1.5) || az < 250 || az > 270 {
		t.Fatalf("Sun at sunset is az %v alt %v", az, alt)
	}
	_, alt = s.SunAltAz(time.Date(2026, 3, 20, 18, 37, 0, 0, time.UTC))
	if !near(alt, 90-s.Lat, 1.5) {
		t.Fatalf("equinox noon Sun altitude %v, want about %v", alt, 90-s.Lat)
	}
}
