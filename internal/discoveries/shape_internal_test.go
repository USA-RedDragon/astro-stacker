package discoveries

import (
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const (
	testBlue  = "Blue"
	testGreen = "Green"
)

type shapeFrame struct {
	night  bool
	filter string
	ratio  float64
	angle  float64
}

// readShapes measures the star shapes of Cygnis Loop Panel 1's R, G and B
// subs from stamps of eight bright stars cut from them, registered: the
// nine subs of 2026-09-02 (Blue, Green, Red, each in time order), then
// every seventh sub of its other nights.
func readShapes(t *testing.T) []shapeFrame {
	t.Helper()
	f, err := os.Open("testdata/shapes-cygnus-loop-p1-2026-09-02.bin.zst")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var hdr [3]int32
	if err := binary.Read(zr, binary.LittleEndian, &hdr); err != nil {
		t.Fatal(err)
	}
	n, stars, side := int(hdr[0]), int(hdr[1]), int(hdr[2])
	const pad = 8
	cell := side + 2*pad
	filters := []string{filterRed, testGreen, testBlue}
	out := make([]shapeFrame, 0, n)
	for range n {
		var fh [2]int32
		if err := binary.Read(zr, binary.LittleEndian, &fh); err != nil {
			t.Fatal(err)
		}
		p := NewPlane(cell*stars, cell)
		anchors := make([]Anchor, 0, stars)
		for s := range stars {
			var scale float32
			px := make([]int16, side*side)
			if err := binary.Read(zr, binary.LittleEndian, &scale); err != nil {
				t.Fatal(err)
			}
			if err := binary.Read(zr, binary.LittleEndian, px); err != nil {
				t.Fatal(err)
			}
			for k, v := range px {
				p.Data[(pad+k/side)*p.W+s*cell+pad+k%side] = float32(v) * scale
			}
			anchors = append(anchors, Anchor{X: float64(s*cell + pad + side/2), Y: float64(pad + side/2)})
		}
		r, a := starShape(p, anchors, 5.5)
		out = append(out, shapeFrame{night: fh[0] == 1, filter: filters[fh[1]], ratio: r, angle: a})
	}
	return out
}

// TestElongatedStarsMakeNoChange is candidate 255622 (Cygnis Loop Panel 1,
// R, G and B, 2026-09-02): the night's stars were elongated, and the change
// was the residual between two stars. Its subs' light, as run 355 stored
// it, on the subs' measured star shapes must be rejected for elongated
// stars, and the same light on subs of other nights must not.
func TestElongatedStarsMakeNoChange(t *testing.T) {
	t.Parallel()
	frames := readShapes(t)
	light := map[string][2][]float64{
		filterRed: {{0.000412, 0.000364, 0.001338}, {0.000538, 0.000535, 0.000563}},
		testGreen: {{0.000911, 0.001599, 0.003231}, {0.000441, 0.000445, 0.000517}},
		testBlue:  {{0.001264, 0.001942, 0.001981}, {0.000426, 0.000421, 0.000465}},
	}
	ref := map[string][]float64{}
	for _, f := range frames {
		if !f.night {
			ref[f.filter] = append(ref[f.filter], f.ratio)
		}
	}
	lim := map[string]float64{}
	for f, v := range ref {
		lim[f] = elongLimit(v)
		t.Logf("%s: %d reference subs, 90th percentile axis ratio %.3f", f, len(v), lim[f])
	}
	build := func(night bool, skip int) *cand {
		c := &cand{}
		c.Sign = 1
		seen := map[string]int{}
		for _, f := range frames {
			if f.night != night {
				continue
			}
			if !night && skip > 0 {
				skip--
				continue
			}
			i := seen[f.filter]
			if i >= 3 {
				continue
			}
			seen[f.filter]++
			c.fluxes, c.errs = append(c.fluxes, light[f.filter][0][i]), append(c.errs, light[f.filter][1][i])
			c.shape, c.angle, c.shapeLim = append(c.shape, f.ratio), append(c.angle, f.angle), append(c.shapeLim, lim[f.filter])
			c.Light = append(c.Light, LightPoint{Filter: f.filter})
			if night {
				t.Logf("%s sub: axis ratio %.3f at %.0f deg", f.filter, f.ratio, f.angle)
			}
		}
		return c
	}
	if why := build(true, 0).elongated(); why == "" {
		t.Error("2026-09-02 is not elongated")
	} else {
		t.Log(why)
	}
	for _, skip := range []int{0, 20, 40} {
		if c := build(false, skip); len(c.fluxes) == 9 {
			if why := c.elongated(); why != "" {
				t.Errorf("other nights' subs from %d: %s", skip, why)
			}
		}
	}
}

// TestElongatedButInRoundSubs: a change also held in the night's subs of
// round stars stands.
func TestElongatedButInRoundSubs(t *testing.T) {
	t.Parallel()
	c := &cand{}
	for k := range 8 {
		r := 1.4
		if k >= 4 {
			r = 1.05
		}
		c.fluxes, c.errs = append(c.fluxes, 1), append(c.errs, 0.3)
		c.shape, c.angle, c.shapeLim = append(c.shape, r), append(c.angle, 60), append(c.shapeLim, 1.2)
		c.Light = append(c.Light, LightPoint{Filter: testNarrowband})
	}
	c.fluxes[7] = 0
	if why := c.elongated(); why != "" {
		t.Errorf("held in the round subs: %s", why)
	}
	for k := 4; k < 8; k++ {
		c.fluxes[k] = 0
	}
	if why := c.elongated(); why == "" {
		t.Error("in the elongated subs alone: not elongated")
	}
	if r, _ := starShape(NewPlane(10, 10), nil, 4); !math.IsNaN(r) {
		t.Errorf("no stars: %.2f", r)
	}
}
