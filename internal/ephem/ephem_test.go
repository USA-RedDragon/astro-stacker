package ephem_test

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

type vector struct {
	At   time.Time `json:"at"`
	Body string    `json:"body"`
	RA   float64   `json:"ra"`
	Dec  float64   `json:"dec"`
	AU   float64   `json:"au"`
	Alt  float64   `json:"alt"`
}

func bodyNamed(t *testing.T, name string) ephem.Body {
	t.Helper()
	for _, b := range append(ephem.Planets(), ephem.Sun, ephem.Moon) {
		if b.String() == name {
			return b
		}
	}
	t.Fatalf("no body %s", name)
	return 0
}

func sepArcsec(ra1, dec1, ra2, dec2 float64) float64 {
	r := math.Pi / 180
	c := math.Sin(dec1*r)*math.Sin(dec2*r) + math.Cos(dec1*r)*math.Cos(dec2*r)*math.Cos((ra1-ra2)*r)
	return math.Acos(math.Min(1, c)) / r * 3600
}

// The vectors are Skyfield 1.x with the full DE440s kernel, astrometric
// topocentric positions from a synthetic site (35 N, 110 W, 1000 m).
func TestObserveMatchesSkyfield(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/skyfield-topocentric.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Site sky.Site `json:"site"`
		Rows []vector `json:"rows"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	e, err := ephem.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range fx.Rows {
		b := bodyNamed(t, v.Body)
		p, err := e.Observe(b, ephem.NewObserver(fx.Site, v.At))
		if err != nil {
			t.Fatalf("%s %s: %v", v.Body, v.At, err)
		}
		limit := 0.05
		if b == ephem.Moon {
			limit = 0.5
		}
		if s := sepArcsec(p.RA, p.Dec, v.RA, v.Dec); s > limit {
			t.Errorf("%s %s: %.2f arcsec from Skyfield, limit %.2f", v.Body, v.At.Format(time.RFC3339), s, limit)
		}
		if math.Abs(p.Distance-v.AU)/v.AU > 1e-6 {
			t.Errorf("%s %s: distance %.9f au, Skyfield %.9f", v.Body, v.At.Format(time.RFC3339), p.Distance, v.AU)
		}
		if math.Abs(p.Altitude-v.Alt) > 0.02 {
			t.Errorf("%s %s: altitude %.4f, Skyfield %.4f", v.Body, v.At.Format(time.RFC3339), p.Altitude, v.Alt)
		}
	}
}

func TestMagnitudesAreInTheirUsualRanges(t *testing.T) {
	t.Parallel()
	e, err := ephem.Load()
	if err != nil {
		t.Fatal(err)
	}
	site := sky.Site{Latitude: 35, Longitude: -110}
	ranges := map[ephem.Body][2]float64{
		ephem.Mercury: {-2.5, 6}, ephem.Venus: {-4.9, -3.7}, ephem.Mars: {-3, 2},
		ephem.Jupiter: {-3, -1.6}, ephem.Saturn: {-0.6, 1.5}, ephem.Uranus: {5.3, 6.1}, ephem.Neptune: {7.6, 8},
		ephem.Moon: {-13, -2},
	}
	for d := range 400 {
		at := time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		o := ephem.NewObserver(site, at)
		for b, r := range ranges {
			p, err := e.Observe(b, o)
			if err != nil {
				t.Fatal(err)
			}
			if p.Magnitude < r[0] || p.Magnitude > r[1] {
				t.Errorf("%s on %s: magnitude %.2f outside %v", b, at.Format(time.DateOnly), p.Magnitude, r)
			}
		}
	}
}

func TestRangeIsTheEmbeddedSlice(t *testing.T) {
	t.Parallel()
	e, err := ephem.Load()
	if err != nil {
		t.Fatal(err)
	}
	from, to := e.Range()
	if from.After(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) || to.Before(time.Date(2091, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("range %s to %s does not cover 1970 to 2090", from, to)
	}
	if !e.Covers(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) || e.Covers(time.Date(2095, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("Covers is wrong")
	}
	_, err = e.Observe(ephem.Mars, ephem.NewObserver(sky.Site{}, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)))
	if !errors.Is(err, ephem.ErrOutOfRange) {
		t.Errorf("2100: %v, want ErrOutOfRange", err)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := ephem.Read([]byte("nope")); err == nil {
		t.Error("garbage was read")
	}
}
