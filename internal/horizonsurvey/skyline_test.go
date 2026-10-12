package horizonsurvey_test

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey/sim"
)

type synthCase struct {
	name   string
	mode   string
	cam    sim.Camera
	expMS  int
	theta  float64
	parity int
	wcs    bool
}

func renderSurvey(t *testing.T, c synthCase) (*hs.Skyline, float64) {
	t.Helper()
	site := testSite()
	when := duskTime()
	if c.mode == hs.ModeNight {
		when = nightTime()
	}
	cam := c.cam
	cam.Theta, cam.Parity = c.theta, c.parity
	var frames [][]hs.FrameBin
	maxTilt := 0.0
	seed := uint64(1)
	for _, az := range []float64{12, 57, 96, 163, 205, 250, 312} {
		for alt := 1.0; alt <= 31; alt += 1.4 {
			ha, dec := site.AltAzToHADec(az, alt)
			pier := hs.PierEast
			if ha < 0 {
				pier = hs.PierWest
			}
			g := cam.Geometry(site.Lat, ha, dec, pier)
			pix := sim.Render(g, cam, sim.DemoSkyline, c.expMS, seed)
			seed++
			var geo hs.Geometry
			geo = g
			if c.wcs {
				geo = hs.NewWCSGeometry(cam.W, cam.H, site, hs.WCSFromGeometry(g, site, when), when)
			}
			cx, cy := float64(cam.W-1)/2, float64(cam.H-1)/2
			uaz, ualt := hs.AltAz(g.Dir(cx, cy-20))
			caz, calt := hs.AltAz(g.Dir(cx, cy))
			daz := math.Mod(uaz-caz+540, 360) - 180
			tilt := math.Abs(math.Atan2(daz*math.Cos(calt*math.Pi/180), ualt-calt) * 180 / math.Pi)
			maxTilt = math.Max(maxTilt, tilt)
			skyRef := cam.SkyPerMS * float64(c.expMS)
			seg := hs.Segment(pix, cam.W, cam.H, c.mode, skyRef)
			frames = append(frames, hs.FrameSkyline(geo, seg.Mask))
		}
	}
	return hs.Combine(frames, 0, 35), maxTilt
}

func TestSyntheticSkylineWithinHalfDegree(t *testing.T) {
	t.Parallel()
	cases := []synthCase{
		{name: "dusk", mode: hs.ModeDusk, cam: smallCamera(sim.DuskCamera()), expMS: 20, theta: 23, parity: 1},
		{name: "dusk mirrored rotated", mode: hs.ModeDusk, cam: smallCamera(sim.DuskCamera()), expMS: 20, theta: 117, parity: -1},
		{name: "night", mode: hs.ModeNight, cam: smallCamera(sim.NightCamera()), expMS: 3000, theta: 23, parity: 1},
		{name: "night mirrored rotated", mode: hs.ModeNight, cam: smallCamera(sim.NightCamera()), expMS: 3000, theta: 250, parity: -1},
		{name: "night plate solved", mode: hs.ModeNight, cam: smallCamera(sim.NightCamera()), expMS: 3000, theta: 300, parity: 1, wcs: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			sl, tilt := renderSurvey(t, c)
			worst, edges, at := checkSkyline(sl, c.cam, sim.DemoSkyline)
			t.Logf("%d edge bins, worst error %.3f deg at az %.2f, largest horizon tilt in a frame %.1f deg", edges, worst, at, tilt)
			if edges < 120 {
				t.Fatalf("only %d edge bins measured", edges)
			}
			if tilt < 20 {
				t.Fatalf("frames were not rotated enough to test field rotation (max tilt %.1f)", tilt)
			}
			if worst > 0.5 {
				t.Fatalf("worst error %.3f deg at az %.2f exceeds 0.5 deg", worst, at)
			}
		})
	}
}

func TestHRZMatchesNINAFormat(t *testing.T) {
	t.Parallel()
	sl, _ := renderSurvey(t, synthCase{mode: hs.ModeDusk, cam: smallCamera(sim.DuskCamera()), expMS: 20, theta: 23, parity: 1})
	pts := sl.Points(1)
	var buf bytes.Buffer
	if err := hs.WriteHRZ(&buf, pts, []string{"test"}); err != nil {
		t.Fatal(err)
	}
	seen := map[float64]float64{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Fields(line)
		if len(cols) != 2 {
			t.Fatalf("line %q does not have two columns", line)
		}
		az, err1 := strconv.ParseFloat(cols[0], 64)
		alt, err2 := strconv.ParseFloat(cols[1], 64)
		if err1 != nil || err2 != nil || alt < 0 || alt > 90 || az < 0 || az > 360 {
			t.Fatalf("line %q is not a valid azimuth altitude pair", line)
		}
		seen[az] = alt
	}
	if len(seen) != 361 {
		t.Fatalf("got %d points, want 361", len(seen))
	}
	if seen[0] != seen[360] {
		t.Fatalf("azimuth 0 (%v) and 360 (%v) differ", seen[0], seen[360])
	}
	if seen[57] < 17.5 {
		t.Fatalf("the 18 deg wall at azimuth 40-75 reads %v at 57", seen[57])
	}
}
