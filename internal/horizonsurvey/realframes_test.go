package horizonsurvey_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	hs "github.com/USA-RedDragon/astro-stacker/internal/horizonsurvey"
	"github.com/klauspost/compress/zstd"
)

type realFrame struct {
	name    string
	row     int
	alt     float64
	expMS   int
	ra, dec float64
	skyRef  float64
	starRef float64
}

func realFrames() []realFrame {
	return []realFrame{
		{"hs_000_20_00_2000ms", 20, 25.60, 2000, 129.825, 80.59972, 0, 0},
		{"hs_000_08_00_2000ms", 8, 12.77, 2000, 131.5583, 67.77084, 0.075, 0.007},
		{"hs_000_05_00_2000ms", 5, 9.56, 2000, 131.7375, 64.56111, 0.1, 0.005},
		{"hs_000_03_01_500ms", 3, 7.42, 500, 132.4083, 62.42083, 0.027, 0.004},
	}
}

func realFramePath(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "night", name+".fits.zst"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(dec); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".fits")
	if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func timeForLST(site hs.Site, lst float64, near time.Time) time.Time {
	t := near
	for range 4 {
		d := math.Mod(lst-site.LST(t)+540, 360) - 180
		t = t.Add(time.Duration(d / 360.9856 * 24 * float64(time.Hour)))
	}
	return t
}

func writeRealRun(t *testing.T, dir string, frameDir func(name string) string) hs.Site {
	t.Helper()
	site := testSite()
	pc := hs.PlanConfig{AzStep: 10, AltMin: 0, AltMax: 35, Overlap: 0.3, FieldW: 2.72, FieldH: 1.53}
	h := hs.RunHeader{Started: time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC), Site: site, Mode: hs.ModeNight, PixelScale: 5.09, FrameW: 1920, FrameH: 1080, Plan: pc, Safety: hs.DefaultSafety()}
	if err := hs.WriteJSON(filepath.Join(dir, hs.RunFile), h); err != nil {
		t.Fatal(err)
	}
	man, err := hs.CreateManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = man.Close() }()
	for _, f := range realFrames() {
		src := frameDir(f.name)
		if filepath.Dir(src) != dir {
			b, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f.name+".fits"), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		lst := math.Mod(f.ra+180, 360)
		start := timeForLST(site, lst, h.Started)
		rec := hs.FrameRecord{
			File: f.name + ".fits", Row: f.row, TargetAz: 0, TargetAlt: f.alt, Start: start, ExposureMS: f.expMS, Mode: hs.ModeNight, PixelScale: 5.09,
			Mount:  hs.MountState{RAHours: f.ra / 15, Dec: f.dec, SiderealHours: lst / 15, PierSide: hs.PierWest},
			SkyRef: f.skyRef, StarRef: f.starRef,
		}
		if err := man.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
	return site
}

func segmentReal(t *testing.T, f realFrame) hs.Segmentation {
	t.Helper()
	im, _, err := hs.LoadFrame(realFramePath(t, t.TempDir(), f.name))
	if err != nil {
		t.Fatal(err)
	}
	return hs.Segment(im.Plane(0), im.W, im.H, hs.SegmentOptions{Mode: hs.ModeNight, SkyRef: f.skyRef, StarDensity: f.starRef})
}

func TestRealNightFramesClassify(t *testing.T) {
	t.Parallel()
	want := map[string][2]float64{
		"hs_000_20_00_2000ms": {0, 0},
		"hs_000_08_00_2000ms": {0, 0},
		"hs_000_05_00_2000ms": {0.33, 0.43},
		"hs_000_03_01_500ms":  {1, 1},
	}
	for _, f := range realFrames() {
		seg := segmentReal(t, f)
		w := want[f.name]
		t.Logf("%s: %s, obstructed %.3f, %d stars, sky level %.4f from %s", f.name, seg.Class, seg.ObstFrac, seg.Stars, seg.SkyLevel, seg.RefBasis)
		if seg.ObstFrac < w[0]-0.003 || seg.ObstFrac > w[1]+0.003 {
			t.Fatalf("%s: obstructed fraction %.3f, want %.2f..%.2f", f.name, seg.ObstFrac, w[0], w[1])
		}
		switch {
		case w[1] == 0 && seg.Class != hs.ClassSky:
			t.Fatalf("%s: class %s, want sky", f.name, seg.Class)
		case w[0] == 1 && seg.Class != hs.ClassObst:
			t.Fatalf("%s: class %s, want obstructed", f.name, seg.Class)
		case w[0] > 0 && w[1] < 1 && seg.Class != hs.ClassEdge:
			t.Fatalf("%s: class %s, want edge", f.name, seg.Class)
		}
	}
}

func TestRealNightFrameBoundary(t *testing.T) {
	t.Parallel()
	seg := segmentReal(t, realFrames()[2])
	m := seg.Mask
	for _, c := range [][2]float64{{0.1, 0.283}, {0.3, 0.33}, {0.5, 0.383}, {0.7, 0.43}, {0.9, 0.48}} {
		by := int(c[0] * float64(m.BH))
		bx := 0
		for bx < m.BW && !m.Sky[by*m.BW+bx] {
			bx++
		}
		got := float64(bx) / float64(m.BW)
		if math.Abs(got-c[1]) > 0.03 {
			t.Fatalf("row %.0f%%: obstruction ends at %.3f of the width, want %.3f", 100*c[0], got, c[1])
		}
		for x := bx; x < m.BW; x++ {
			if !m.Sky[by*m.BW+x] {
				t.Fatalf("row %.0f%%: obstruction again at %.3f, right of the boundary", 100*c[0], float64(x)/float64(m.BW))
			}
		}
	}
}

func TestShortExposureBelowSkyIsObstruction(t *testing.T) {
	t.Parallel()
	f := realFrames()[3]
	f.starRef = 0
	if seg := segmentReal(t, f); seg.Class != hs.ClassObst {
		t.Fatalf("a 500 ms frame at %.3f of the expected sky level came back %s (%.3f obstructed)", seg.Median/f.skyRef, seg.Class, seg.ObstFrac)
	}
}

func TestRealNightFramesAnalyze(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := t.TempDir()
	writeRealRun(t, dir, func(name string) string { return realFramePath(t, src, name) })
	res, err := hs.Analyze(hs.AnalyzeOptions{Dirs: []string{dir}, HRZStep: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Calibration == nil || res.Calibration.Frames != 2 || res.Calibration.Parity != -1 {
		t.Fatalf("calibration %+v, want one from the two solved frames", res.Calibration)
	}
	classes := map[string]string{}
	for _, f := range res.Frames {
		classes[f.Record.File] = f.Class
		if f.Err != nil {
			t.Fatalf("%s: %v", f.Record.File, f.Err)
		}
	}
	want := map[string]string{"hs_000_20_00_2000ms.fits": hs.ClassSky, "hs_000_08_00_2000ms.fits": hs.ClassSky, "hs_000_05_00_2000ms.fits": hs.ClassEdge, "hs_000_03_01_500ms.fits": hs.ClassObst}
	for k, v := range want {
		if classes[k] != v {
			t.Fatalf("%s came back %s, want %s", k, classes[k], v)
		}
	}
	edges := 0
	for _, b := range res.Skyline.Bins {
		if b.Kind != hs.BinEdge || !b.Measured || b.Edges == 0 {
			continue
		}
		edges++
		if b.Alt < 4.5 || b.Alt > 6.5 || math.Abs(math.Mod(b.Az+180, 360)-180) > 4 {
			t.Fatalf("edge at az %.2f alt %.2f, want alt 4.5-6.5 near az 0", b.Az, b.Alt)
		}
	}
	if edges < 5 {
		t.Fatalf("only %d edge bins measured", edges)
	}
}

func TestStarlessFrameWithoutReferenceIsObstruction(t *testing.T) {
	t.Parallel()
	const w, h = 480, 270
	pix := make([]float32, w*h)
	for i := range pix {
		pix[i] = 0.002 + 0.0004*float32((i*7919)%13)/13
	}
	seg := hs.Segment(pix, w, h, hs.SegmentOptions{Mode: hs.ModeNight})
	if seg.Class != hs.ClassObst {
		t.Fatalf("a dark starless frame with no sky reference came back %s (%.3f obstructed)", seg.Class, seg.ObstFrac)
	}
}
