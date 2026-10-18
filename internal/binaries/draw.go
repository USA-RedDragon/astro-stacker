package binaries

import (
	"image"
	"image/color"
	"math"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//nolint:gochecknoglobals // the parsed fonts, shared by every picture
var fonts = sync.OnceValues(func() ([2]*opentype.Font, error) {
	r, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return [2]*opentype.Font{}, err
	}
	b, err := opentype.Parse(gobold.TTF)
	return [2]*opentype.Font{r, b}, err
})

type faceKey struct {
	bold bool
	size float64
}

//nolint:gochecknoglobals // font faces made once per size
var (
	faceMu sync.Mutex
	faces  = map[faceKey]font.Face{}
)

func face(size float64, bold bool) font.Face {
	faceMu.Lock()
	defer faceMu.Unlock()
	k := faceKey{bold, size}
	if f, ok := faces[k]; ok {
		return f
	}
	fs, err := fonts()
	if err != nil {
		return nil
	}
	i := 0
	if bold {
		i = 1
	}
	f, err := opentype.NewFace(fs[i], &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	faces[k] = f
	return f
}

// canvas draws on an RGBA image with simple anti-aliased marks.
type canvas struct {
	img *image.RGBA
}

func newCanvas(w, h int, bg color.RGBA) *canvas {
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, w, h))}
	for i := 0; i < len(c.img.Pix); i += 4 {
		c.img.Pix[i], c.img.Pix[i+1], c.img.Pix[i+2], c.img.Pix[i+3] = bg.R, bg.G, bg.B, bg.A
	}
	return c
}

// blend paints col over pixel x, y with coverage a, 0 to 1.
func (c *canvas) blend(x, y int, col color.RGBA, a float64) {
	if !(image.Point{x, y}.In(c.img.Rect)) || a <= 0 {
		return
	}
	a = math.Min(a, 1) * float64(col.A) / 255
	i := c.img.PixOffset(x, y)
	p := c.img.Pix[i : i+4 : i+4]
	p[0] = uint8(float64(p[0])*(1-a) + float64(col.R)*a + 0.5)
	p[1] = uint8(float64(p[1])*(1-a) + float64(col.G)*a + 0.5)
	p[2] = uint8(float64(p[2])*(1-a) + float64(col.B)*a + 0.5)
	p[3] = 255
}

// disc fills a circle of radius r at x, y.
func (c *canvas) disc(x, y, r float64, col color.RGBA) {
	for j := int(math.Floor(y - r - 1)); j <= int(math.Ceil(y+r+1)); j++ {
		for i := int(math.Floor(x - r - 1)); i <= int(math.Ceil(x+r+1)); i++ {
			d := math.Hypot(float64(i)+0.5-x, float64(j)+0.5-y)
			c.blend(i, j, col, r+0.5-d)
		}
	}
}

// ring strokes a circle of radius r and line width w at x, y.
func (c *canvas) ring(x, y, r, w float64, col color.RGBA) {
	for j := int(math.Floor(y - r - w - 1)); j <= int(math.Ceil(y+r+w+1)); j++ {
		for i := int(math.Floor(x - r - w - 1)); i <= int(math.Ceil(x+r+w+1)); i++ {
			d := math.Abs(math.Hypot(float64(i)+0.5-x, float64(j)+0.5-y) - r)
			c.blend(i, j, col, w/2+0.5-d)
		}
	}
}

// line strokes a segment of width w.
func (c *canvas) line(x0, y0, x1, y1, w float64, col color.RGBA) {
	minX, maxX := math.Min(x0, x1)-w-1, math.Max(x0, x1)+w+1
	minY, maxY := math.Min(y0, y1)-w-1, math.Max(y0, y1)+w+1
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	for j := int(minY); j <= int(maxY); j++ {
		for i := int(minX); i <= int(maxX); i++ {
			px, py := float64(i)+0.5, float64(j)+0.5
			t := 0.0
			if l2 > 0 {
				t = math.Max(0, math.Min(1, ((px-x0)*dx+(py-y0)*dy)/l2))
			}
			d := math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
			c.blend(i, j, col, w/2+0.5-d)
		}
	}
}

// rect strokes a rectangle outline of width w.
func (c *canvas) rect(x0, y0, x1, y1, w float64, col color.RGBA) {
	c.line(x0, y0, x1, y0, w, col)
	c.line(x1, y0, x1, y1, w, col)
	c.line(x1, y1, x0, y1, w, col)
	c.line(x0, y1, x0, y0, w, col)
}

// textWidth is the advance of s at size.
func textWidth(s string, size float64, bold bool) float64 {
	f := face(size, bold)
	if f == nil {
		return 0
	}
	return float64(font.MeasureString(f, s)) / 64
}

// text draws s with its left baseline at x, y; align 0 left, 0.5 centre,
// 1 right.
func (c *canvas) text(x, y float64, s string, size float64, bold bool, col color.RGBA, align float64) {
	f := face(size, bold)
	if f == nil {
		return
	}
	x -= align * textWidth(s, size, bold)
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: f, Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}}
	d.DrawString(s)
}

// halo draws s white over a soft dark outline, readable on any picture.
func (c *canvas) halo(x, y float64, s string, size float64, bold bool, align float64) {
	r := math.Max(1, size/9)
	shadow := color.RGBA{0, 0, 0, 90}
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				c.text(x+dx, y+dy, s, size, bold, shadow, align)
			}
		}
	}
	c.text(x, y, s, size, bold, color.RGBA{255, 255, 255, 255}, align)
}
