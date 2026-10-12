package bodies_test

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type truth struct {
	ra, dec, mag, r, delta float64
}

func horizonsGeo(t *testing.T, name string) truth {
	t.Helper()
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(readFile(t, name)), &resp); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(resp.Result, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "$$SOE" {
			continue
		}
		f := strings.Split(lines[i+1], ",")
		num := func(j int) float64 {
			v, err := strconv.ParseFloat(strings.TrimSpace(f[j]), 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		return truth{ra: num(3), dec: num(4), mag: num(5), r: num(7), delta: num(9)}
	}
	t.Fatal("no rows")
	return truth{}
}

func separation(ra1, dec1, ra2, dec2 float64) float64 {
	r := math.Pi / 180
	c := math.Sin(dec1*r)*math.Sin(dec2*r) + math.Cos(dec1*r)*math.Cos(dec2*r)*math.Cos((ra1-ra2)*r)
	return math.Acos(math.Min(1, c)) / r * 3600
}

func TestTwoBodyAgainstHorizons(t *testing.T) {
	t.Parallel()
	els, bad := bodies.ParseComets(readFile(t, "CometEls-sample.txt"))
	if bad != 0 || len(els) != 9 {
		t.Fatalf("%d elements, %d unreadable", len(els), bad)
	}
	by := map[string]bodies.Elements{}
	for _, el := range els {
		by[el.Designation] = el
	}
	jpl, err := bodies.ParseSBDB(readFile(t, "sbdb-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for des, file := range map[string]string{
		swan:           "horizons-geo-c2025r2.json",
		"C/2024 E1":    "horizons-geo-c2024e1.json",
		"C/2014 UN271": "horizons-geo-c2014un271.json",
		"24P":          "horizons-geo-p24.json",
		"C/2024 J3":    "horizons-geo-c2024j3.json",
	} {
		el, ok := by[des]
		if !ok {
			t.Fatalf("no elements for %s", des)
		}
		want := horizonsGeo(t, file)
		got := el.Seen(at)
		sep := separation(got.RA, got.Dec, want.ra, want.dec)
		t.Logf("%s: %.1f arcsec off, r %.5f/%.5f, delta %.5f/%.5f", des, sep, got.R, want.r, got.Delta, want.delta)
		if sep > 60 {
			t.Errorf("%s is %.1f arcsec from Horizons", des, sep)
		}
		if math.Abs(got.R-want.r) > 1e-3 || math.Abs(got.Delta-want.delta) > 1e-3 {
			t.Errorf("%s r %v delta %v, Horizons %v %v", des, got.R, got.Delta, want.r, want.delta)
		}
		p, ok := jpl[des]
		if !ok {
			t.Fatalf("no SBDB magnitude parameters for %s", des)
		}
		if m, ok := bodies.JPLMagnitude(p, got); !ok || math.Abs(m-want.mag) > 0.01 {
			t.Errorf("%s JPL magnitude %.3f, Horizons T-mag %.3f", des, m, want.mag)
		}
	}
}

func TestMagnitudeAndParsing(t *testing.T) {
	t.Parallel()
	els, _ := bodies.ParseComets(readFile(t, "CometEls-sample.txt"))
	by := map[string]bodies.Elements{}
	for _, el := range els {
		by[el.Designation] = el
	}
	r2 := by[swan]
	if r2.Name != "C/2025 R2 (SWAN)" || r2.OrbitType != "C" || r2.H == nil || *r2.H != 12.4 || r2.G == nil || *r2.G != 4 {
		t.Fatalf("C/2025 R2 %+v", r2)
	}
	if want := time.Date(2025, 9, 12, 19, 19, 29, 280000000, time.UTC); r2.Perihelion.Sub(want).Abs() > time.Second {
		t.Errorf("perihelion %v", r2.Perihelion)
	}
	m, ok := r2.Magnitude(bodies.Sighting{R: 10, Delta: 100})
	if !ok || math.Abs(m-(12.4+10+10)) > 1e-9 {
		t.Errorf("magnitude %v", m)
	}
	for des, name := range map[string]string{"2P": "2P/Encke", "12P": "12P/Pons-Brooks", r1b: r1b, "C/1947 X1-B": "C/1947 X1-B (Southern comet)"} {
		if by[des].Name != name {
			t.Errorf("%s: %q", des, by[des].Name)
		}
	}
	if _, err := bodies.ParseCometLine("short"); err == nil {
		t.Error("a short line parsed")
	}
}

func TestUniversalPropagationMatchesKepler(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for _, e := range []float64{0.2, 0.9, 0.999, 1.0, 1.001, 1.5, 3} {
		el := bodies.Elements{Q: 1.2, E: e, Perihelion: at.Add(-200 * 24 * time.Hour)}
		got := el.Heliocentric(at)
		want := keplerRadius(1.2, e, 200)
		if r := math.Sqrt(got[0]*got[0] + got[1]*got[1] + got[2]*got[2]); math.Abs(r-want) > 1e-8*want {
			t.Errorf("e %v: r %v, Kepler %v", e, r, want)
		}
	}
}

func keplerRadius(q, e, days float64) float64 {
	k := 0.01720209895
	switch {
	case e < 1:
		a := q / (1 - e)
		m := k / math.Pow(a, 1.5) * days
		ea := m
		for range 100 {
			ea -= (ea - e*math.Sin(ea) - m) / (1 - e*math.Cos(ea))
		}
		return a * (1 - e*math.Cos(ea))
	case e > 1:
		a := q / (e - 1)
		m := k / math.Pow(a, 1.5) * days
		h := math.Asinh(m / e)
		for range 100 {
			h -= (e*math.Sinh(h) - h - m) / (e*math.Cosh(h) - 1)
		}
		return a * (e*math.Cosh(h) - 1)
	}
	w := 3 * k / math.Sqrt(2*q*q*q) * days
	y := math.Cbrt(w/2 + math.Sqrt(w*w/4+1))
	s := y - 1/y
	return q * (1 + s*s)
}

func TestParseSBDB(t *testing.T) {
	t.Parallel()
	m, err := bodies.ParseSBDB(readFile(t, "sbdb-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	for des, want := range map[string][2]float64{"C/2025 R2": {13.9, 9.5}, "2P": {15.7, 4.5}, "161P": {15.2, 4.5}, r1b: {9.6, 10}} {
		if m[des] != want {
			t.Errorf("%s: %v, want %v", des, m[des], want)
		}
	}
	if _, ok := m["C/1947 X1-B"]; ok {
		t.Error("a comet without M1 and K1 was kept")
	}
	if _, err := bodies.ParseSBDB(`{"fields":["pdes"],"data":[]}`); err == nil {
		t.Error("a query without M1 parsed")
	}
}

func TestHyperbolicPropagationFarFromPerihelion(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for _, o := range []struct{ q, e float64 }{{0.255, 1.2}, {1.36, 6.14}, {0.3, 1.2}, {3.57, 1.0022}, {11, 1.004}} {
		for _, days := range []float64{1, 100, 5940, 14340, 40000, -5940, -40000} {
			el := bodies.Elements{Q: o.q, E: o.e, Perihelion: at.Add(-time.Duration(days * 86400e9))}
			got := el.Heliocentric(at)
			r := math.Sqrt(got[0]*got[0] + got[1]*got[1] + got[2]*got[2])
			want := keplerRadius(o.q, o.e, days)
			if math.IsNaN(r) || math.Abs(r-want) > 1e-7*want {
				t.Errorf("q %v e %v %v days: r %v, Kepler %v", o.q, o.e, days, r, want)
			}
			if s := el.Seen(at); !s.Finite() {
				t.Errorf("q %v e %v %v days: sighting %+v", o.q, o.e, days, s)
			}
		}
	}
}
