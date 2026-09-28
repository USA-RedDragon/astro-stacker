package stacking

import (
	"math"
	"testing"
)

func TestTANRoundTrip(t *testing.T) {
	g := wcs{ra0: 313.02, dec0: 31.5, px0: 3124.5, py0: 2088.5, width: 6248, height: 4176,
		cd: [2][2]float64{{-0.000534 * 0.99, 0.000534 * 0.14}, {0.000534 * 0.14, 0.000534 * 0.99}}}
	for _, p := range [][2]float64{{1, 1}, {6248, 4176}, {3000, 100}} {
		ra, dec := g.toSky(p[0], p[1])
		x, y, ok := g.toPixel(ra, dec)
		if !ok || math.Abs(x-p[0]) > 1e-6 || math.Abs(y-p[1]) > 1e-6 {
			t.Errorf("%v -> %v,%v -> %v,%v", p, ra, dec, x, y)
		}
	}
}

// sky is a smooth pattern on the sky, so any panel sampling it at the
// right place gets the same value.
func sky(ra, dec float64) float32 {
	return float32(1 + 0.3*math.Sin(ra*8) + 0.3*math.Cos(dec*9))
}

// synthPanel renders sky() into a panel centred at (ra, dec), top row first.
func synthPanel(ra, dec float64, w, h int, scale float64) layoutPanel {
	g := wcs{ra0: ra, dec0: dec, px0: float64(w+1) / 2, py0: float64(h+1) / 2, width: w, height: h,
		cd: [2][2]float64{{-scale, 0}, {0, scale}}}
	data := make([]float32, w*h)
	for r := range h {
		for c := range w {
			sr, sd := g.toSky(float64(c+1), float64(h-r))
			data[r*w+c] = sky(sr, sd)
		}
	}
	return layoutPanel{WCS: g, Data: data, W: w, H: h, Bin: 1}
}

func TestLayoutPlacesPanelsAndLeavesMissingOnesBlack(t *testing.T) {
	const w, h, scale = 120, 80, 0.01 // 1.2° x 0.8° panels
	// Three panels in a row along declination; the middle one has no
	// master yet.
	p1 := synthPanel(83.0, 20.0, w, h, scale)
	p2 := synthPanel(83.0, 20.7, w, h, scale)
	p2.Data = nil
	p3 := synthPanel(83.0, 21.4, w, h, scale)
	panels := []layoutPanel{p1, p2, p3}
	l, bin := newLayout(panels, scale, 1000)
	if bin != 1 || l.H < 2*h || l.W < w-2 {
		t.Fatalf("layout %+v, bin %d: not all three panels fit", l, bin)
	}
	canvas := l.render(panels)
	// Within a panel the canvas follows the sky there: backgrounds are
	// matched between panels by an offset, so compare differences.
	// The canvas value at the pixel nearest (ra, dec), and the sky at that
	// pixel's centre.
	at := func(ra, dec float64) (float64, float64) {
		c, r, _ := l.toCanvas(ra, dec)
		col, row := math.Round(c), math.Round(r)
		sra, sdec := l.toSky(col, row)
		return float64(canvas[int(row)*l.W+int(col)]), float64(sky(sra, sdec))
	}
	for _, pair := range [][2][2]float64{
		{{83.0, 19.75}, {83.3, 20.0}},  // panel 1 alone
		{{83.1, 21.45}, {82.8, 21.65}}, // panel 3 alone
	} {
		a, b := pair[0], pair[1]
		va, sa := at(a[0], a[1])
		vb, sb := at(b[0], b[1])
		got, want := vb-va, sb-sa
		if math.Abs(got-want) > 0.01 {
			t.Errorf("sky from %v to %v changes by %.3f on the canvas, %.3f in the panel", a, b, got, want)
		}
	}
	// The middle panel's own area, beyond its neighbours' overlap, is black.
	c, r, _ := l.toCanvas(83.0, 20.7)
	if v := canvas[int(r)*l.W+int(c)]; v != 0 {
		t.Errorf("missing panel's centre is %v, want 0", v)
	}
	// Its outline is drawn onto a stretched copy.
	img := l.stretchedWithOutlines(canvas, panels)
	edges := 0
	for _, v := range img {
		if v == 0.35 {
			edges++
		}
	}
	if edges < 2*(w+h)/2 {
		t.Errorf("drew %d outline pixels for the missing panel", edges)
	}
}
