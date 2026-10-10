package mosaics_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func TestSun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		at      time.Time
		ra, dec float64
		tol     float64
	}{
		{"meeus example 25a", time.Date(1992, 10, 13, 0, 0, 0, 0, time.UTC), 198.38083, -7.78507, 0.02},
		{"march equinox 2026", time.Date(2026, 3, 20, 14, 46, 0, 0, time.UTC), 0, 0, 0.05},
		{"june solstice 2026", time.Date(2026, 6, 21, 8, 24, 0, 0, time.UTC), 90, 23.436, 0.05},
		{"september equinox 2026", time.Date(2026, 9, 23, 0, 5, 0, 0, time.UTC), 180, 0, 0.05},
		{"december solstice 2026", time.Date(2026, 12, 21, 20, 50, 0, 0, time.UTC), 270, -23.436, 0.05},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ra, dec := mosaics.Sun(tc.at)
			if angDiff(ra, tc.ra) > tc.tol*3 || !near(dec, tc.dec, tc.tol) {
				t.Fatalf("sun at %v,%v, want %v,%v", ra, dec, tc.ra, tc.dec)
			}
			if ra < 0 || ra >= 360 {
				t.Fatalf("RA %v outside [0,360)", ra)
			}
		})
	}
}

func TestAltitude(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		site     mosaics.Site
		ra, dec  float64
		min, max float64
	}{
		{"pole star sits at the latitude", mosaics.Site{Lat: 32.8, Lon: -97.3}, 0, 90, 32.8, 32.8},
		{"zenith passage", mosaics.Site{Lat: 32.8, Lon: -97.3}, 120, 32.8, 2*32.8 - 90, 90},
		{"never rises", mosaics.Site{Lat: 32.8, Lon: -97.3}, 200, -80, -42.8, -22.8},
		{"southern circumpolar", mosaics.Site{Lat: -31.3, Lon: 149}, 50, -70, 11.3, 51.3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lo, hi := 100.0, -100.0
			for m := 0; m < 24*60; m += 2 {
				a := mosaics.Altitude(start.Add(time.Duration(m)*time.Minute), tc.site, tc.ra, tc.dec)
				lo, hi = min(lo, a), max(hi, a)
			}
			if !near(lo, tc.min, 0.3) || !near(hi, tc.max, 0.3) {
				t.Fatalf("altitude range %v..%v, want %v..%v", lo, hi, tc.min, tc.max)
			}
		})
	}
}

func TestDarkHours(t *testing.T) {
	t.Parallel()
	texas := mosaics.Site{Lat: 32.8, Lon: -97.3}
	australia := mosaics.Site{Lat: -31.3, Lon: 149}
	pole := []mosaics.Point{{RA: 0, Dec: 90}}
	cases := []struct {
		name   string
		night  time.Time
		site   mosaics.Site
		points []mosaics.Point
		minAlt float64
		lo, hi float64
	}{
		{"december night in texas", time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), texas, pole, 20, 10.5, 11.5},
		{"june night in texas", time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), texas, pole, 20, 5.8, 7},
		{"june night in australia", time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), australia, []mosaics.Point{{RA: 0, Dec: -90}}, 20, 10.5, 11.5},
		{"no points means sky only", time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), texas, nil, 20, 10.5, 11.5},
		{"below the minimum", time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), texas, pole, 40, 0, 0},
		{"never rises", time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), texas, []mosaics.Point{{RA: 0, Dec: -80}}, 0, 0, 0},
		{"orion in winter", time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), texas, []mosaics.Point{{RA: 83.8, Dec: -5.4}}, 30, 4.5, 7},
		{"orion in summer", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), texas, []mosaics.Point{{RA: 83.8, Dec: -5.4}}, 30, 0, 0},
		{"one point sets early", time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), texas, []mosaics.Point{{RA: 83.8, Dec: -5.4}, {RA: 350, Dec: 10}}, 30, 0.25, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := mosaics.DarkHours(tc.night, tc.site, tc.points, tc.minAlt)
			if h < tc.lo || h > tc.hi {
				t.Fatalf("%v dark hours, want %v..%v", h, tc.lo, tc.hi)
			}
		})
	}
}

func TestMonthlyDarkHours(t *testing.T) {
	t.Parallel()
	texas := mosaics.Site{Lat: 32.8, Lon: -97.3}
	cases := []struct {
		name       string
		points     []mosaics.Point
		best, zero []int
	}{
		{"orion", []mosaics.Point{{RA: 83.8, Dec: -5.4}}, []int{0, 11}, []int{5, 6}},
		{"cygnus", []mosaics.Point{{RA: 312, Dec: 31}}, []int{6, 7}, []int{1, 2}},
		{"pole", []mosaics.Point{{RA: 0, Dec: 90}}, []int{11, 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := mosaics.MonthlyDarkHours(2026, texas, tc.points, 30)
			top := 0.0
			for _, h := range m {
				top = max(top, h)
				if h < 0 || h > 24 {
					t.Fatalf("month hours %v", m)
				}
			}
			for _, i := range tc.best {
				if m[i] < top*0.8 {
					t.Fatalf("month %d has %v of a best %v: %v", i+1, m[i], top, m)
				}
			}
			for _, i := range tc.zero {
				if m[i] > 0.5 {
					t.Fatalf("month %d has %v hours: %v", i+1, m[i], m)
				}
			}
		})
	}
	m := mosaics.MonthlyDarkHours(2026, texas, []mosaics.Point{{RA: 0, Dec: 90}}, 20)
	want := 0.0
	for _, d := range []int{1, 8, 15, 22} {
		want += mosaics.DarkHours(time.Date(2026, 3, d, 0, 0, 0, 0, time.UTC), texas, []mosaics.Point{{RA: 0, Dec: 90}}, 20) / 4
	}
	if !near(m[2], want, 1e-9) {
		t.Fatalf("march %v, want the mean of four nights %v", m[2], want)
	}
}

func TestSeason(t *testing.T) {
	t.Parallel()
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name string
		at   time.Time
		ra   float64
		opp  time.Time
	}{
		{"autumn object in autumn", day(2026, 10, 9), 0, day(2026, 9, 22)},
		{"winter object in summer", day(2026, 7, 1), 90, day(2026, 12, 21)},
		{"winter object in late winter", day(2026, 3, 1), 90, day(2025, 12, 21)},
		{"orion", time.Date(2026, 10, 9, 18, 30, 0, 0, time.UTC), 83.8, day(2026, 12, 15)},
		{"cygnus", day(2027, 1, 15), 312, day(2026, 8, 3)},
		{"negative ra", day(2026, 10, 9), -30, day(2026, 8, 20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, end := mosaics.Season(tc.at, tc.ra)
			opp := start.AddDate(0, 6, 0)
			if d := opp.Sub(tc.opp).Hours() / 24; d < -3 || d > 3 {
				t.Fatalf("opposition %v, want about %v", opp, tc.opp)
			}
			if !end.Equal(opp.AddDate(0, 6, 0)) {
				t.Fatalf("season %v..%v is not a year around %v", start, end, opp)
			}
			if tc.at.Before(start) || tc.at.After(end) {
				t.Fatalf("season %v..%v does not contain %v", start, end, tc.at)
			}
			sunRA, _ := mosaics.Sun(opp)
			if angDiff(sunRA, tc.ra+180) > 1.5 {
				t.Fatalf("sun at %v on %v, want %v", sunRA, opp, tc.ra+180)
			}
		})
	}
}
