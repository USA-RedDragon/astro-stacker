package stacking

import (
	"fmt"
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/frameheader"
)

// wcs is a TAN (gnomonic) plate solution; SIP distortion is ignored, which
// at preview scale is well under a pixel.
type wcs struct {
	ra0, dec0     float64       // CRVAL, degrees
	px0, py0      float64       // CRPIX, 1-based FITS pixels (y up)
	cd            [2][2]float64 // degrees per pixel
	width, height int
}

func wcsFromHeader(kw frameheader.Keywords, w, h int) (wcs, error) {
	g := wcs{ra0: kw.Float("CRVAL1"), dec0: kw.Float("CRVAL2"), px0: kw.Float("CRPIX1"), py0: kw.Float("CRPIX2"), width: w, height: h}
	if cd := kw.Float("CD1_1"); !math.IsNaN(cd) {
		g.cd = [2][2]float64{{cd, kw.Float("CD1_2")}, {kw.Float("CD2_1"), kw.Float("CD2_2")}}
	} else {
		c1, c2 := kw.Float("CDELT1"), kw.Float("CDELT2")
		g.cd = [2][2]float64{{c1 * kw.Float("PC1_1"), c1 * kw.Float("PC1_2")}, {c2 * kw.Float("PC2_1"), c2 * kw.Float("PC2_2")}}
	}
	for _, v := range []float64{g.ra0, g.dec0, g.px0, g.py0, g.cd[0][0], g.cd[0][1], g.cd[1][0], g.cd[1][1]} {
		if math.IsNaN(v) {
			return g, fmt.Errorf("no plate solution in the header")
		}
	}
	return g, nil
}

const deg = math.Pi / 180

// project gives the tangent-plane coordinates (degrees) of (ra, dec) around
// (ra0, dec0); ok is false on the far side of the sky.
func project(ra0, dec0, ra, dec float64) (xi, eta float64, ok bool) {
	d0, d, da := dec0*deg, dec*deg, (ra-ra0)*deg
	cosc := math.Sin(d0)*math.Sin(d) + math.Cos(d0)*math.Cos(d)*math.Cos(da)
	if cosc <= 0 {
		return 0, 0, false
	}
	xi = math.Cos(d) * math.Sin(da) / cosc / deg
	eta = (math.Cos(d0)*math.Sin(d) - math.Sin(d0)*math.Cos(d)*math.Cos(da)) / cosc / deg
	return xi, eta, true
}

// deproject is project's inverse.
func deproject(ra0, dec0, xi, eta float64) (ra, dec float64) {
	x, y, d0 := xi*deg, eta*deg, dec0*deg
	den := math.Cos(d0) - y*math.Sin(d0)
	ra = ra0 + math.Atan2(x, den)/deg
	dec = math.Atan2(math.Sin(d0)+y*math.Cos(d0), math.Hypot(x, den)) / deg
	return ra, dec
}

// toSky maps a FITS pixel (1-based, y up) to sky coordinates.
func (g wcs) toSky(x, y float64) (float64, float64) {
	dx, dy := x-g.px0, y-g.py0
	return deproject(g.ra0, g.dec0, g.cd[0][0]*dx+g.cd[0][1]*dy, g.cd[1][0]*dx+g.cd[1][1]*dy)
}

// toPixel maps sky coordinates to a FITS pixel; ok is false off the sky.
func (g wcs) toPixel(ra, dec float64) (x, y float64, ok bool) {
	xi, eta, ok := project(g.ra0, g.dec0, ra, dec)
	if !ok {
		return 0, 0, false
	}
	det := g.cd[0][0]*g.cd[1][1] - g.cd[0][1]*g.cd[1][0]
	dx := (g.cd[1][1]*xi - g.cd[0][1]*eta) / det
	dy := (-g.cd[1][0]*xi + g.cd[0][0]*eta) / det
	return dx + g.px0, dy + g.py0, true
}

// scale is the pixel size in degrees.
func (g wcs) scale() float64 {
	return math.Sqrt(math.Abs(g.cd[0][0]*g.cd[1][1] - g.cd[0][1]*g.cd[1][0]))
}

// corners are the sky positions of the frame's corners.
func (g wcs) corners() [4][2]float64 {
	var c [4][2]float64
	for i, p := range [4][2]float64{{0.5, 0.5}, {float64(g.width) + 0.5, 0.5}, {float64(g.width) + 0.5, float64(g.height) + 0.5}, {0.5, float64(g.height) + 0.5}} {
		c[i][0], c[i][1] = g.toSky(p[0], p[1])
	}
	return c
}

// placed moves g's frame to (ra, dec) turned by rot degrees, for a panel
// with no master, framed like a solved one.
func (g wcs) placed(ra, dec, rot float64) wcs {
	s, c := math.Sin(rot*deg), math.Cos(rot*deg)
	out := g
	out.ra0, out.dec0 = ra, dec
	out.px0, out.py0 = float64(g.width+1)/2, float64(g.height+1)/2
	// Rotating the sky frame turns the linear transform.
	out.cd = [2][2]float64{
		{c*g.cd[0][0] - s*g.cd[1][0], c*g.cd[0][1] - s*g.cd[1][1]},
		{s*g.cd[0][0] + c*g.cd[1][0], s*g.cd[0][1] + c*g.cd[1][1]},
	}
	return out
}

// layoutPanel is one panel for the layout: its plate solution and, when it
// has a master, its image binned to the canvas scale (top row first).
type layoutPanel struct {
	WCS  wcs
	Data []float32 // nil for a panel without a master
	W, H int       // binned size
	Bin  int
}

// layout is a north-up canvas holding every panel of a mosaic.
type layout struct {
	ra0, dec0 float64
	scale     float64 // degrees per canvas pixel
	x0, y0    float64 // tangent-plane offset of the canvas's top-left corner
	W, H      int
}

// newLayout frames all panels, present or not, at no more than maxWidth
// canvas pixels across, at a whole multiple of the panels' pixel scale.
func newLayout(panels []layoutPanel, nativeScale float64, maxWidth int) (layout, int) {
	var ras, decs []float64
	for _, p := range panels {
		ras = append(ras, p.WCS.ra0)
		decs = append(decs, p.WCS.dec0)
	}
	l := layout{ra0: meanRA(ras), dec0: mean(decs)}
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range panels {
		for _, c := range p.WCS.corners() {
			xi, eta, ok := project(l.ra0, l.dec0, c[0], c[1])
			if !ok {
				continue
			}
			minX, maxX = min(minX, xi), max(maxX, xi)
			minY, maxY = min(minY, eta), max(maxY, eta)
		}
	}
	bin := max(1, int(math.Ceil((maxX-minX)/nativeScale/float64(maxWidth))))
	l.scale = nativeScale * float64(bin)
	// East is left on a north-up sky image: xi grows to the left.
	l.x0, l.y0 = maxX, maxY
	l.W = int(math.Ceil((maxX - minX) / l.scale))
	l.H = int(math.Ceil((maxY - minY) / l.scale))
	return l, bin
}

func (l layout) toSky(col, row float64) (float64, float64) {
	return deproject(l.ra0, l.dec0, l.x0-(col+0.5)*l.scale, l.y0-(row+0.5)*l.scale)
}

func (l layout) toCanvas(ra, dec float64) (col, row float64, ok bool) {
	xi, eta, ok := project(l.ra0, l.dec0, ra, dec)
	return (l.x0-xi)/l.scale - 0.5, (l.y0-eta)/l.scale - 0.5, ok
}

// render reprojects the panels with masters onto the canvas, matching their
// backgrounds and feathering their edges. Pixels no panel covers are 0.
func (l layout) render(panels []layoutPanel) []float32 {
	sum := make([]float64, l.W*l.H)
	wsum := make([]float64, l.W*l.H)
	var skies []float64
	for _, p := range panels {
		if p.Data != nil {
			med, _ := statsNonZero(p.Data)
			skies = append(skies, med)
		}
	}
	common := mean(skies)
	for _, p := range panels {
		if p.Data == nil {
			continue
		}
		sky, _ := statsNonZero(p.Data)
		feather := 0.08 * float64(min(p.W, p.H))
		minC, maxC, minR, maxR := l.bounds(p.WCS)
		for row := minR; row <= maxR; row++ {
			for col := minC; col <= maxC; col++ {
				ra, dec := l.toSky(float64(col), float64(row))
				x, y, ok := p.WCS.toPixel(ra, dec)
				if !ok {
					continue
				}
				// FITS pixel to binned, top-row-first coordinates.
				bc := (x-0.5)/float64(p.Bin) - 0.5
				br := (float64(p.WCS.height)-y+0.5)/float64(p.Bin) - 0.5
				v, ok := bilinear(p.Data, p.W, p.H, bc, br)
				if !ok {
					continue
				}
				edge := min(bc, br, float64(p.W-1)-bc, float64(p.H-1)-br)
				w := math.Max(1e-3, math.Min(1, edge/feather))
				i := row*l.W + col
				sum[i] += w * (float64(v) - sky + common)
				wsum[i] += w
			}
		}
	}
	out := make([]float32, l.W*l.H)
	for i := range out {
		if wsum[i] > 0 {
			out[i] = float32(sum[i] / wsum[i])
			if out[i] == 0 {
				out[i] = math.SmallestNonzeroFloat32 // 0 means uncovered
			}
		}
	}
	return out
}

// bounds is the canvas box a panel's frame falls in.
func (l layout) bounds(g wcs) (minC, maxC, minR, maxR int) {
	minC, minR, maxC, maxR = l.W, l.H, -1, -1
	for _, c := range g.corners() {
		col, row, ok := l.toCanvas(c[0], c[1])
		if !ok {
			continue
		}
		minC, maxC = min(minC, int(math.Floor(col))), max(maxC, int(math.Ceil(col)))
		minR, maxR = min(minR, int(math.Floor(row))), max(maxR, int(math.Ceil(row)))
	}
	return max(0, minC), min(l.W-1, maxC), max(0, minR), min(l.H-1, maxR)
}

// outlines draws the frames of panels without masters onto a stretched
// canvas, so the missing panels show where they go.
func (l layout) outlines(img []float32, panels []layoutPanel, value float32) {
	for _, p := range panels {
		if p.Data != nil {
			continue
		}
		c := p.WCS.corners()
		for i := range 4 {
			a, b := c[i], c[(i+1)%4]
			ac, ar, ok1 := l.toCanvas(a[0], a[1])
			bc, br, ok2 := l.toCanvas(b[0], b[1])
			if !ok1 || !ok2 {
				continue
			}
			n := int(math.Hypot(bc-ac, br-ar)) + 1
			for k := 0; k <= n; k++ {
				t := float64(k) / float64(n)
				col, row := int(math.Round(ac+t*(bc-ac))), int(math.Round(ar+t*(br-ar)))
				for _, d := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
					cc, rr := col+d[0], row+d[1]
					if cc >= 0 && cc < l.W && rr >= 0 && rr < l.H && img[rr*l.W+cc] == 0 {
						img[rr*l.W+cc] = value
					}
				}
			}
		}
	}
}

func bilinear(data []float32, w, h int, x, y float64) (float32, bool) {
	if x < 0 || y < 0 || x > float64(w-1) || y > float64(h-1) {
		return 0, false
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	// Empty (0) neighbours, a rejected hot pixel or the registration
	// border, are left out; with none left the point is uncovered.
	var sum, wsum float32
	for _, n := range [4]struct{ v, w float32 }{
		{data[y0*w+x0], (1 - fx) * (1 - fy)}, {data[y0*w+x1], fx * (1 - fy)},
		{data[y1*w+x0], (1 - fx) * fy}, {data[y1*w+x1], fx * fy},
	} {
		if n.v != 0 {
			sum += n.v * n.w
			wsum += n.w
		}
	}
	if wsum < 0.25 {
		return 0, false
	}
	return sum / wsum, true
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// meanRA averages right ascensions across the 0/360 wrap.
func meanRA(v []float64) float64 {
	var s, c float64
	for _, x := range v {
		s += math.Sin(x * deg)
		c += math.Cos(x * deg)
	}
	ra := math.Atan2(s, c) / deg
	if ra < 0 {
		ra += 360
	}
	return ra
}

// binImage averages factor×factor blocks of a top-row-first image.
func binImage(data []float32, w, h, factor int) ([]float32, int, int) {
	if factor <= 1 {
		return data, w, h
	}
	bw, bh := w/factor, h/factor
	out := make([]float32, bw*bh)
	for by := range bh {
		for bx := range bw {
			var s float32
			n := 0
			for y := by * factor; y < (by+1)*factor; y++ {
				for x := bx * factor; x < (bx+1)*factor; x++ {
					if v := data[y*w+x]; v != 0 {
						s += v
						n++
					}
				}
			}
			if n > factor*factor/2 {
				out[by*bw+bx] = s / float32(n)
			}
		}
	}
	return out, bw, bh
}

// stretchedWithOutlines stretches a canvas for a preview and draws where
// missing panels go.
func (l layout) stretchedWithOutlines(canvas []float32, panels []layoutPanel) []float32 {
	s := stretchNonZero(canvas)
	l.outlines(s, panels, 0.35)
	return s
}
