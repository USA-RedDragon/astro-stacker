package moon_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

const testLat, testLon = 35.0, -110.0

// Against JPL Horizons, topocentric, from a made-up test site (35 N, 110 W);
// the Horizons answers are in testdata.
func TestAltitudeMatchesHorizons(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at       string
		ra, dec  float64 // topocentric apparent, differs by up to the parallax
		altitude float64
	}{
		{"2025-10-20T03:00:00Z", 189.338905, -6.966455, -39.748509},
		{"2026-03-14T06:30:00Z", 298.726229, -24.845452, -53.591080},
		{"2024-12-25T10:30:00Z", 208.866349, -14.762582, 9.449174},
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		p := moon.At(at)
		if alt := p.Altitude(at, testLat, testLon); math.Abs(alt-c.altitude) > 0.5 {
			t.Errorf("%s: altitude %.2f, want %.2f", c.at, alt, c.altitude)
		}
		if s := p.Separation(c.ra, c.dec); s > 1.5 {
			t.Errorf("%s: %.2f° from Horizons' position", c.at, s)
		}
		topo := p.Topocentric(at, testLat, testLon)
		if s := topo.Separation(c.ra, c.dec); s > 0.1 {
			t.Errorf("%s: topocentric %.2f° from Horizons' position", c.at, s)
		}
		if alt := topo.Altitude(at, testLat, testLon); math.Abs(alt-c.altitude) > 0.1 {
			t.Errorf("%s: topocentric altitude %.2f, want %.2f", c.at, alt, c.altitude)
		}
	}
}

func TestAge(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at  string
		age float64
	}{
		{"2025-10-21T12:25:00Z", 0},             // new moon
		{"2025-11-05T13:19:00Z", 29.530588 / 2}, // full moon
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		age := moon.At(at).Age
		if d := math.Min(math.Abs(age-c.age), 29.530588-math.Abs(age-c.age)); d > 0.5 {
			t.Errorf("%s: age %.2f d, want %.2f", c.at, age, c.age)
		}
	}
}

func TestAvoidance(t *testing.T) {
	t.Parallel()
	full := moon.Position{Age: 29.530588 / 2}
	if a := full.Avoidance(30, 7); math.Abs(a-30) > 1e-9 {
		t.Errorf("full moon avoidance %v, want 30", a)
	}
	week := moon.Position{Age: 29.530588/2 - 7}
	if a := week.Avoidance(30, 7); math.Abs(a-15) > 1e-9 {
		t.Errorf("a width from full %v, want 15", a)
	}
}
