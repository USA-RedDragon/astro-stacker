package mosaics_test

import (
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

func TestAltAzMeeusExample13b(t *testing.T) {
	t.Parallel()
	washington := mosaics.Site{Lat: 38 + 55/60.0 + 17/3600.0, Lon: -(77 + 3/60.0 + 56/3600.0)}
	ra := (23 + 9/60.0 + 16.641/3600.0) * 15
	dec := -(6 + 43/60.0 + 11.61/3600.0)
	alt, az := mosaics.AltAz(time.Date(1987, 4, 10, 19, 21, 0, 0, time.UTC), washington, ra, dec)
	if !near(alt, 15.1249, 0.01) || !near(az, 68.0337+180, 0.01) {
		t.Fatalf("alt %v az %v, want 15.1249 and 248.0337 (Meeus 68.0337 from south)", alt, az)
	}
}

func TestAltAzCardinalDirections(t *testing.T) {
	t.Parallel()
	site := mosaics.Site{Lat: 33, Lon: 0}
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	lst := 280.46061837 + 360.98564736629*(2461100.5-2451545)
	cases := []struct {
		name    string
		ra, dec float64
		az      float64
	}{
		{"transit south of zenith", lst, -10, 180},
		{"transit north of zenith", lst, 70, 0},
		{"equator rising", lst + 90, 0, 90},
		{"equator setting", lst - 90, 0, 270},
	}
	for _, tc := range cases {
		_, az := mosaics.AltAz(at, site, tc.ra, tc.dec)
		d := az - tc.az
		if d > 180 {
			d -= 360
		}
		if d < -180 {
			d += 360
		}
		if !near(d, 0, 0.05) {
			t.Errorf("%s: azimuth %v, want %v", tc.name, az, tc.az)
		}
	}
}

func horizon(f func(az int) float64) *mosaics.Horizon {
	h := &mosaics.Horizon{Step: 1, Altitudes: make([]float64, 360)}
	for i := range h.Altitudes {
		h.Altitudes[i] = f(i)
	}
	return h
}

func TestHorizonInterpolatesAndWraps(t *testing.T) {
	t.Parallel()
	h := horizon(func(az int) float64 { return float64(az % 10) })
	if !h.Valid() {
		t.Fatal("valid horizon rejected")
	}
	cases := []struct{ az, want float64 }{{0, 0}, {3, 3}, {3.25, 3.25}, {9.5, 4.5}, {359, 9}, {359.5, 4.5}, {360, 0}, {-1, 9}, {725, 5}}
	for _, tc := range cases {
		if got := h.At(tc.az); !near(got, tc.want, 1e-9) {
			t.Errorf("At(%v) = %v, want %v", tc.az, got, tc.want)
		}
	}
	for _, bad := range []*mosaics.Horizon{nil, {Step: 1}, {Step: 1, Altitudes: make([]float64, 359)}, {Step: 0, Altitudes: make([]float64, 360)}, horizon(func(int) float64 { return 91 })} {
		if bad.Valid() {
			t.Errorf("invalid horizon %+v accepted", bad)
		}
	}
}

func TestLimitMirrorsTargetSchedulerHorizonDefinition(t *testing.T) {
	t.Parallel()
	h := horizon(func(az int) float64 { return float64(az) / 4 })
	cases := []struct {
		name  string
		limit mosaics.Limit
		az    float64
		want  float64
	}{
		{"flat ignores azimuth", mosaics.Flat(20), 300, 20},
		{"horizon plus offset", mosaics.Custom(h, 5, 10), 100, 30},
		{"minimum altitude floor", mosaics.Custom(h, 5, 40), 100, 40},
		{"capped at 90", mosaics.Custom(h, 20, 0), 300, 90},
		{"low horizon uses minimum", mosaics.Custom(h, 0, 15), 8, 15},
	}
	for _, tc := range cases {
		if got := tc.limit.At(tc.az); !near(got, tc.want, 1e-9) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFlatDarkHoursUnchanged(t *testing.T) {
	t.Parallel()
	site := mosaics.Site{Lat: 32.9, Lon: -105.5}
	cases := []struct {
		pt     mosaics.Point
		minAlt float64
		want   [12]float64
	}{
		{mosaics.Point{RA: 83.8, Dec: -5.4}, 15, [12]float64{7.958333333333332, 5.541666666666666, 3.333333333333333, 0.8541666666666665, 0, 0, 0, 0.3958333333333333, 2.75, 5.041666666666666, 7.499999999999999, 9.0625}},
		{mosaics.Point{RA: 312, Dec: 44}, 30, [12]float64{0.20833333333333331, 0, 0.6458333333333333, 1.958333333333333, 3.270833333333333, 5, 6.541666666666666, 7.520833333333333, 6.999999999999999, 5.6875, 4.104166666666666, 2.1875}},
		{mosaics.Point{RA: 270, Dec: -24}, 0, [12]float64{0.20833333333333331, 1.7708333333333333, 3.104166666666666, 4.416666666666666, 5.729166666666666, 6.354166666666666, 6.333333333333333, 5.166666666666666, 3.8749999999999996, 2.5625, 0.9791666666666666, 0}},
	}
	for _, tc := range cases {
		got := mosaics.MonthlyDarkHours(2026, site, []mosaics.Point{tc.pt}, mosaics.Flat(tc.minAlt))
		if got != tc.want {
			t.Errorf("%v above %v: %v, want %v", tc.pt, tc.minAlt, got, tc.want)
		}
		zero := mosaics.MonthlyDarkHours(2026, site, []mosaics.Point{tc.pt}, mosaics.Custom(horizon(func(int) float64 { return 0 }), 0, tc.minAlt))
		if zero != tc.want {
			t.Errorf("%v: a flat zero custom horizon gave %v, want %v", tc.pt, zero, tc.want)
		}
	}
}

func TestSouthernHorizonCutsOnlySouthernTargets(t *testing.T) {
	t.Parallel()
	site := mosaics.Site{Lat: 32.9, Lon: -105.5}
	south := horizon(func(az int) float64 {
		if az >= 100 && az <= 260 {
			return 60
		}
		return 0
	})
	night := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	sgr := []mosaics.Point{{RA: 270, Dec: -24}}
	cep := []mosaics.Point{{RA: 330, Dec: 60}}
	flatS := mosaics.DarkHours(night, site, sgr, mosaics.Flat(15))
	custS := mosaics.DarkHours(night, site, sgr, mosaics.Custom(south, 0, 15))
	if flatS < 4 || custS > 0.5 {
		t.Fatalf("southern target: %v hours flat, %v behind a 60° southern horizon", flatS, custS)
	}
	flatN := mosaics.DarkHours(night, site, cep, mosaics.Flat(15))
	custN := mosaics.DarkHours(night, site, cep, mosaics.Custom(south, 0, 15))
	if flatN < 4 || custN != flatN {
		t.Fatalf("northern target: %v hours flat, %v with a southern horizon", flatN, custN)
	}
	offset := mosaics.DarkHours(night, site, cep, mosaics.Custom(south, 50, 15))
	if offset >= flatN {
		t.Fatalf("a 50° offset did not cut the northern target: %v vs %v", offset, flatN)
	}
}
