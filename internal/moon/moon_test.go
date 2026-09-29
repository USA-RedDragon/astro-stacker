package moon_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/moon"
)

// Against JPL Horizons, topocentric, from the observatory (31.546944 N,
// 99.382222 W).
func TestAltitudeMatchesHorizons(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at       string
		ra, dec  float64 // topocentric apparent, differs by up to the parallax
		altitude float64
	}{
		{"2025-10-20T03:00:00Z", 189.42682, -6.90803, -49.126195},
		{"2026-03-14T06:30:00Z", 298.85821, -24.83778, -44.816406},
		{"2024-12-25T09:15:00Z", 208.35034, -14.43458, 5.089370},
	} {
		at, _ := time.Parse(time.RFC3339, c.at)
		p := moon.At(at)
		if alt := p.Altitude(at, 31.546944, -99.382222); math.Abs(alt-c.altitude) > 0.5 {
			t.Errorf("%s: altitude %.2f, want %.2f", c.at, alt, c.altitude)
		}
		if s := p.Separation(c.ra, c.dec); s > 1.5 {
			t.Errorf("%s: %.2f° from Horizons' position", c.at, s)
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
