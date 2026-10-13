package horizonsurvey_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

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
