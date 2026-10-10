package sky_test

import (
	"math"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

func testSite() sky.Site { return sky.Site{Latitude: 31.5, Longitude: -99.4, Elevation: 473} }

func TestSunPosition(t *testing.T) {
	t.Parallel()
	ra, dec := sky.SunPosition(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC))
	if math.Abs(dec-23.44) > 0.1 || math.Abs(ra-90) > 1.5 {
		t.Errorf("solstice sun at %.2f %.2f", ra, dec)
	}
	_, dec = sky.SunPosition(time.Date(2026, 3, 20, 15, 0, 0, 0, time.UTC))
	if math.Abs(dec) > 0.5 {
		t.Errorf("equinox sun dec %.2f", dec)
	}
}

func TestAltitudeOfPolaris(t *testing.T) {
	t.Parallel()
	s := testSite()
	for h := range 24 {
		alt := s.Altitude(37.95, 89.26, time.Date(2026, 10, 9, h, 0, 0, 0, time.UTC))
		if math.Abs(alt-s.Latitude) > 1 {
			t.Errorf("Polaris at %.1f° at %02d:00", alt, h)
		}
	}
}

func TestNightOf(t *testing.T) {
	t.Parallel()
	s := testSite()
	n := s.NightOf(time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC), 5*time.Minute)
	if n.Dusk == nil || n.Dawn == nil {
		t.Fatal("no astronomical night in October at 31.5° N")
	}
	if h := n.DarkHours(); h < 9 || h > 11.5 {
		t.Errorf("October dark hours %.1f", h)
	}
	if n.Dusk.UTC().Hour() < 0 || n.Dusk.Before(n.Start) || !n.Dawn.After(*n.Dusk) {
		t.Errorf("dusk %v dawn %v start %v", n.Dusk, n.Dawn, n.Start)
	}
	m31 := n.Window(10.68, 41.27, 30)
	if m31.Hours < 6 || m31.PeakAlt == nil || *m31.PeakAlt < 75 || *m31.PeakAlt > 82 {
		t.Errorf("M31 window %+v", m31)
	}
	if n.HoursAbove(10.68, -80, 30) != 0 {
		t.Error("a far-south object rose above 30°")
	}
	if f := n.MoonIllumination(); f < 0 || f > 1 {
		t.Errorf("moon illumination %v", f)
	}
	if d := n.MoonSeparation(10.68, 41.27); d == nil || *d < 0 || *d > 180 {
		t.Errorf("moon separation %v", d)
	}
}

func TestYearHours(t *testing.T) {
	t.Parallel()
	y := testSite().Year(2026, 20*time.Minute)
	m42 := y.Hours(83.82, -5.39, 30)
	if m42[0] < 4 || m42[6] > 0.5 {
		t.Errorf("M42 by month %v", m42)
	}
	if sky.Illumination(0) > 0.01 || sky.Illumination(14.765) < 0.99 {
		t.Error("illumination at new and full moon")
	}
}

func TestCurve(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	c := testSite().Curve(10.68, 41.27, from, from.Add(2*time.Hour), 30*time.Minute)
	if len(c) != 5 {
		t.Errorf("curve has %d points", len(c))
	}
}

func TestFrame(t *testing.T) {
	t.Parallel()
	f := sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}
	if math.Abs(f.Scale()-1.915) > 0.001 {
		t.Errorf("scale %.4f", f.Scale())
	}
	if math.Abs(f.WidthDeg()-3.32) > 0.01 || math.Abs(f.HeightDeg()-2.22) > 0.01 {
		t.Errorf("field %.2f × %.2f", f.WidthDeg(), f.HeightDeg())
	}
	one := f.Fit(178, 63, sky.DefaultOverlap)
	if one.Panels != 1 || one.Category != sky.FitOne || math.Abs(one.Fill-0.894) > 0.01 {
		t.Errorf("M31 fit %+v", one)
	}
	loop := f.Fit(230, 160, sky.DefaultOverlap)
	if loop.Panels < 2 || loop.Panels > 4 || loop.Category != sky.FitFew {
		t.Errorf("Cygnus Loop fit %+v", loop)
	}
	big := f.Fit(1200, 1200, sky.DefaultOverlap)
	if big.Category != sky.FitMany {
		t.Errorf("Barnard's Loop fit %+v", big)
	}
	if c := f.Coverage(5.36773, 3.5788); math.Abs(c-0.383) > 0.01 {
		t.Errorf("coverage %.3f", c)
	}
	if (sky.Frame{}).Scale() != 0 {
		t.Error("empty frame has a scale")
	}
}

func TestLocalNoon(t *testing.T) {
	t.Parallel()
	s := testSite()
	at := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	noon := s.LocalNoon(at)
	if !noon.Before(at) || at.Sub(noon) > 24*time.Hour {
		t.Errorf("noon %v for %v", noon, at)
	}
}

func TestFitRotationLaysTheLongSideAlongTheMajorAxis(t *testing.T) {
	t.Parallel()
	f := sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}
	if r := f.Fit(190, 60, sky.DefaultOverlap).Rotation(35); r != 125 {
		t.Errorf("M31 at PA 35 wants the camera at 125, got %v", r)
	}
	if r := f.Fit(10, 5, sky.DefaultOverlap).Rotation(10); r != 100 {
		t.Errorf("got %v", r)
	}
	if r := f.Fit(10, 5, sky.DefaultOverlap).Rotation(170); r != 80 {
		t.Errorf("got %v", r)
	}
}

func TestNightWithoutDarknessHasNoPeakOrMoonSeparation(t *testing.T) {
	t.Parallel()
	n := sky.Site{Latitude: 65, Longitude: 25}.NightOf(time.Date(2026, 6, 21, 18, 0, 0, 0, time.UTC), 0)
	if len(n.Dark) != 0 {
		t.Fatalf("astronomical darkness at 65° N in June: %d samples", len(n.Dark))
	}
	if w := n.Window(10.68, 41.27, 30); w.PeakAlt != nil || w.Hours != 0 {
		t.Errorf("window %+v", w)
	}
	if d := n.MoonSeparation(10.68, 41.27); d != nil {
		t.Errorf("moon separation %v", *d)
	}
	if f := n.MoonIllumination(); f < 0 || f > 1 {
		t.Errorf("moon illumination %v", f)
	}
}
