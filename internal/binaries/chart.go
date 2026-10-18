package binaries

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
)

// ChartGeometry is how the sky lies on a picture around the star: the
// star's pixel, the size of a pixel in arcseconds and the unit pixel
// directions of north and east.
type ChartGeometry struct {
	X, Y        float64
	Scale       float64
	North, East [2]float64
}

const (
	chartSide   = 900
	chartBand   = 70
	chartArcmin = 10.0
)

// FindingChart cuts a square about chartArcmin wide around the star from
// the user's own picture, in grey, and marks the star with two ticks,
// north and east with arrows and an arcminute scale bar, as a PNG.
func FindingChart(src image.Image, g ChartGeometry, name string) ([]byte, string, error) {
	if !(g.Scale > 0) {
		return nil, "", errors.New("the picture has no pixel scale")
	}
	b := src.Bounds()
	if g.X < 0 || g.Y < 0 || g.X >= float64(b.Dx()) || g.Y >= float64(b.Dy()) {
		return nil, "", errors.New("the star is outside your stacked picture")
	}
	half := chartArcmin * 60 / g.Scale / 2
	half = math.Min(half, math.Min(float64(b.Dx()), float64(b.Dy()))/2)
	x0 := math.Max(0, math.Min(float64(b.Dx())-2*half, g.X-half))
	y0 := math.Max(0, math.Min(float64(b.Dy())-2*half, g.Y-half))
	sr := image.Rect(b.Min.X+int(x0), b.Min.Y+int(y0), b.Min.X+int(x0+2*half), b.Min.Y+int(y0+2*half))
	field := 2 * half * g.Scale / 60
	gray := image.NewGray(image.Rect(0, 0, sr.Dx(), sr.Dy()))
	draw.Draw(gray, gray.Bounds(), src, sr.Min, draw.Src)
	stretchGrey(gray)
	c := newCanvas(chartSide, chartSide+chartBand, color.RGBA{255, 255, 255, 255})
	view := image.Rect(0, chartBand, chartSide, chartSide+chartBand)
	xdraw.CatmullRom.Scale(c.img, view, gray, gray.Bounds(), draw.Src, nil)
	k := chartSide / float64(sr.Dx())
	sx, sy := (g.X-x0)*k, (g.Y-y0)*k+chartBand
	red := color.RGBA{220, 30, 30, 255}
	for _, d := range [][2]float64{{-1, 0}, {0, -1}} {
		c.line(sx+d[0]*14, sy+d[1]*14, sx+d[0]*40, sy+d[1]*40, 3, red)
	}
	ax, ay := 100.0, chartBand+float64(chartSide)-100
	arrow := func(dir [2]float64, label string) {
		if dir == ([2]float64{}) {
			return
		}
		ex, ey := ax+dir[0]*50, ay+dir[1]*50
		c.line(ax, ay, ex, ey, 2.5, red)
		c.text(ex+dir[0]*16, ey+dir[1]*16+7, label, 20, true, red, 0.5)
	}
	arrow(unit(g.North), "N")
	arrow(unit(g.East), "E")
	if g.North == ([2]float64{}) || g.East == ([2]float64{}) {
		return nil, "", errors.New("north and east could not be worked out on the picture")
	}
	bar := 60 / g.Scale * k
	bx, by := float64(chartSide)-40-bar, chartBand+float64(chartSide)-40
	c.line(bx, by, bx+bar, by, 3, red)
	c.text(bx+bar/2, by-10, "1'", 20, true, red, 0.5)
	c.text(20, 32, "Finding chart: "+name, 24, true, ink, 0)
	sub := fmt.Sprintf("Your own stacked picture, %.1f' wide. The star is between the ticks.", field)
	c.text(20, 58, sub, 17, false, grey, 0)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, c.img); err != nil {
		return nil, "", err
	}
	caption := fmt.Sprintf("From your own stacked picture, %.1f arcminutes wide, north and east marked.", field)
	return out.Bytes(), caption, nil
}

func unit(v [2]float64) [2]float64 {
	d := math.Hypot(v[0], v[1])
	if d == 0 {
		return v
	}
	return [2]float64{v[0] / d, v[1] / d}
}

// stretchGrey sets the grey picture's black at its median and white at
// its 99.7th percentile, for stars to stand out on a light chart, then
// turns it to black stars on white.
func stretchGrey(g *image.Gray) {
	var hist [256]int
	for _, v := range g.Pix {
		hist[v]++
	}
	n := len(g.Pix)
	at := func(q float64) int {
		want := int(q * float64(n))
		s := 0
		for i, h := range hist {
			s += h
			if s > want {
				return i
			}
		}
		return 255
	}
	lo, hi := at(0.5), at(0.997)
	if hi <= lo {
		hi = lo + 1
	}
	for i, v := range g.Pix {
		t := math.Max(0, math.Min(1, float64(int(v)-lo)/float64(hi-lo)))
		g.Pix[i] = uint8(255 - math.Round(math.Sqrt(t)*255))
	}
}
