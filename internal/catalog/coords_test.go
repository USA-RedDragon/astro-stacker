package catalog_test

import (
	"math"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
)

func TestParseCoordinates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		ra, dec float64
		ok      bool
	}{
		{"20h45m +30.7", 311.25, 30.7, true},
		{"311.3 30.7", 311.3, 30.7, true},
		{"311.3, -30.7", 311.3, -30.7, true},
		{"311.3° +30.7°", 311.3, 30.7, true},
		{"RA 20:45:38 Dec +30:42", 311.4083, 30.7, true},
		{"ra 20h45m38s dec 30d42m", 311.4083, 30.7, true},
		{"00h42m44s +41d16m09s", 10.6833, 41.2692, true},
		{"05h35m17s -05°23′28″", 83.8208, -5.3911, true},
		{"05h35m17s −05°23'28\"", 83.8208, -5.3911, true},
		{"20 45 38 +30 42 00", 311.4083, 30.7, true},
		{"ra=23.987h dec=62.44", 359.805, 62.44, true},
		{"23.987h 62.44", 359.805, 62.44, true},
		{"20h45m 30.7", 311.25, 30.7, true},
		{"20h45m38s +30.7", 311.4083, 30.7, true},
		{"IC 434", 0, 0, false},
		{m31, 0, 0, false},
		{abell85, 0, 0, false},
		{"M 8 and M 20", 0, 0, false},
		{"C/2025 R2", 0, 0, false},
		{"25h00m +10", 0, 0, false},
		{"400 10", 0, 0, false},
		{"10 95", 0, 0, false},
		{"20h45m +30:75", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			ra, dec, ok := catalog.ParseCoordinates(tc.in)
			if ok != tc.ok {
				t.Fatalf("ParseCoordinates(%q) ok = %v", tc.in, ok)
			}
			if ok && (math.Abs(ra-tc.ra) > 0.001 || math.Abs(dec-tc.dec) > 0.001) {
				t.Fatalf("ParseCoordinates(%q) = %f, %f; want %f, %f", tc.in, ra, dec, tc.ra, tc.dec)
			}
		})
	}
}
