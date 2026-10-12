package horizonsurvey

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type canvas struct {
	img               *image.RGBA
	left, top, pw, ph int
	altMin, altMax    float64
}

func (c *canvas) xy(az, alt float64) (int, int) {
	x := c.left + int(math.Round(az/360*float64(c.pw)))
	y := c.top + int(math.Round((c.altMax-alt)/(c.altMax-c.altMin)*float64(c.ph)))
	return x, y
}

func (c *canvas) dot(x, y, r int, col color.Color) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			px, py := x+dx, y+dy
			if image.Pt(px, py).In(c.img.Rect) {
				c.img.Set(px, py, col)
			}
		}
	}
}

func (c *canvas) line(x0, y0, x1, y1, r int, col color.Color, dash int) {
	n := max(abs(x1-x0), abs(y1-y0), 1)
	for i := 0; i <= n; i++ {
		if dash > 0 && (i/dash)%2 == 1 {
			continue
		}
		c.dot(x0+(x1-x0)*i/n, y0+(y1-y0)*i/n, r, col)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (c *canvas) text(x, y int, s string, col color.Color) {
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func (c *canvas) inside(p [2]float64) bool {
	return p[1] >= c.altMin && p[1] <= c.altMax
}

func (c *canvas) polyline(pts [][2]float64, r int, col color.Color, dash int) {
	for i := 1; i < len(pts); i++ {
		if math.Abs(pts[i][0]-pts[i-1][0]) > 180 || !c.inside(pts[i]) || !c.inside(pts[i-1]) {
			continue
		}
		x0, y0 := c.xy(pts[i-1][0], pts[i-1][1])
		x1, y1 := c.xy(pts[i][0], pts[i][1])
		c.line(x0, y0, x1, y1, r, col, dash)
	}
}

func FrameOutline(g Geometry) [][2]float64 {
	w, h := g.Size()
	fw, fh := float64(w-1), float64(h-1)
	corners := [][2]float64{{0, 0}, {fw, 0}, {fw, fh}, {0, fh}, {0, 0}}
	var pts [][2]float64
	for i := 1; i < len(corners); i++ {
		for k := range 24 {
			f := float64(k) / 24
			x := corners[i-1][0]*(1-f) + corners[i][0]*f
			y := corners[i-1][1]*(1-f) + corners[i][1]*f
			az, alt := AltAz(g.Dir(x, y))
			pts = append(pts, [2]float64{az, alt})
		}
	}
	return append(pts, pts[0])
}

type PlotInput struct {
	Skyline  *Skyline
	Outlines [][][2]float64
	Title    string
	Excluded []AzRange
}

func Plot(w io.Writer, in PlotInput) error {
	const W, H = 1800, 640
	altMax := 40.0
	for _, b := range in.Skyline.Bins {
		altMax = math.Max(altMax, math.Ceil(b.Alt/5)*5+5)
	}
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, W, H)), left: 60, top: 40, pw: W - 90, ph: H - 110, altMin: -3, altMax: altMax}
	white := color.RGBA{255, 255, 255, 255}
	for y := range H {
		for x := range W {
			c.img.Set(x, y, white)
		}
	}
	grid := color.RGBA{220, 220, 220, 255}
	ink := color.RGBA{30, 30, 30, 255}
	for az := 0; az <= 360; az += 30 {
		x, _ := c.xy(float64(az), 0)
		c.line(x, c.top, x, c.top+c.ph, 0, grid, 0)
		c.text(x-10, c.top+c.ph+18, fmt.Sprintf("%d", az), ink)
	}
	for alt := 0; float64(alt) <= altMax; alt += 5 {
		_, y := c.xy(0, float64(alt))
		c.line(c.left, y, c.left+c.pw, y, 0, grid, 0)
		c.text(c.left-30, y+4, fmt.Sprintf("%d", alt), ink)
	}
	_, y0 := c.xy(0, 0)
	c.line(c.left, y0, c.left+c.pw, y0, 0, color.RGBA{150, 150, 150, 255}, 0)
	excl := color.RGBA{255, 236, 200, 255}
	for _, r := range in.Excluded {
		for az := 0.0; az < 360; az += 0.2 {
			if r.Contains(az) {
				x, _ := c.xy(az, 0)
				c.line(x, c.top, x, c.top+c.ph, 0, excl, 0)
			}
		}
	}
	shade := color.RGBA{200, 200, 200, 255}
	for _, b := range in.Skyline.Bins {
		x, ytop := c.xy(b.Az, b.Alt)
		_, ybot := c.xy(b.Az, c.altMin)
		c.line(x, ytop, x, ybot, 0, shade, 0)
	}
	cover := color.RGBA{70, 130, 220, 255}
	for _, o := range in.Outlines {
		c.polyline(o, 0, cover, 0)
	}
	measured := color.RGBA{0, 0, 0, 255}
	interp := color.RGBA{230, 120, 0, 255}
	bound := color.RGBA{200, 0, 0, 255}
	var seg [][2]float64
	flush := func(col color.Color, dash int) {
		c.polyline(seg, 1, col, dash)
		seg = seg[:0]
	}
	prev := in.Skyline.Bins[0].Measured
	for _, b := range in.Skyline.Bins {
		if b.Measured != prev {
			last := seg
			if prev {
				flush(measured, 0)
			} else {
				flush(interp, 6)
			}
			if len(last) > 0 {
				seg = append(seg, last[len(last)-1])
			}
			prev = b.Measured
		}
		seg = append(seg, [2]float64{b.Az, b.Alt})
	}
	if prev {
		flush(measured, 0)
	} else {
		flush(interp, 6)
	}
	for _, b := range in.Skyline.Bins {
		if b.Measured && (b.Kind == BinAbove || b.Conflict) {
			x, y := c.xy(b.Az, b.Alt)
			c.dot(x, y, 2, bound)
		}
	}
	c.text(c.left, 24, in.Title, ink)
	ly := H - 30
	legend := []struct {
		col color.Color
		s   string
	}{
		{measured, "measured skyline"},
		{interp, "interpolated (no frame)"},
		{bound, "obstructed to top of frame, or frames disagree"},
		{cover, "frame footprints"},
		{excl, "excluded azimuths"},
	}
	x := c.left
	for _, l := range legend {
		c.line(x, ly-4, x+24, ly-4, 1, l.col, 0)
		c.text(x+30, ly, l.s, ink)
		x += 40 + 7*len(l.s) + 20
	}
	c.text(c.left+c.pw-120, H-50, "azimuth (deg)", ink)
	return png.Encode(w, c.img)
}
