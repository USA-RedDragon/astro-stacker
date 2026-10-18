package binaries

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Mark is what a picture points at: a star at X, Y, or for a moving
// object the track from X, Y to X1, Y1, in picture pixels.
type Mark struct {
	X, Y   float64
	X1, Y1 float64
	Track  bool
}

func (m Mark) centre() (float64, float64) {
	if m.Track {
		return (m.X + m.X1) / 2, (m.Y + m.Y1) / 2
	}
	return m.X, m.Y
}

// box is a rectangle in float pixels.
type box struct{ X0, Y0, X1, Y1 float64 }

func (b box) overlaps(o box) bool {
	return b.X0 < o.X1 && o.X0 < b.X1 && b.Y0 < o.Y1 && o.Y0 < b.Y1
}

func (b box) inside(w, h float64) bool {
	return b.X0 >= 0 && b.Y0 >= 0 && b.X1 <= w && b.Y1 <= h
}

func (b box) contains(x, y float64) bool { return x >= b.X0 && x <= b.X1 && y >= b.Y0 && y <= b.Y1 }

// Layout is where the share picture's parts go.
type Layout struct {
	W, H float64
	// R is the circle's radius; Mark the box the circle or track covers.
	R    float64
	Mark box
	// Inset is the zoomed-in box and Source the part of the picture it
	// shows; Corner is the inset corner the connector lines leave from,
	// the two corners next to the one nearest the mark.
	Inset   box
	Source  box
	Lines   [2][2]float64
	Label   box
	Name    box
	TextPx  float64
	Shifted bool
}

// layoutFor places the circle, the labels and the inset so none of them
// covers the mark or runs off the picture: the inset goes in the corner
// farthest from the mark, the label above the mark and the name below it,
// both moved inward at the edges and swapped or moved beside the mark when
// they would hit the inset or leave the picture.
func layoutFor(w, h float64, m Mark, labelW, nameW float64) (Layout, error) {
	l := Layout{W: w, H: h, R: math.Max(10, w/90), TextPx: math.Max(14, w/55)}
	cx, cy := m.centre()
	if cx < 0 || cy < 0 || cx > w || cy > h {
		return l, errors.New("the mark is outside the picture")
	}
	l.Mark = box{cx - l.R, cy - l.R, cx + l.R, cy + l.R}
	if m.Track {
		l.Mark = box{math.Min(m.X, m.X1) - l.R/2, math.Min(m.Y, m.Y1) - l.R/2, math.Max(m.X, m.X1) + l.R/2, math.Max(m.Y, m.Y1) + l.R/2}
	}
	margin := math.Max(8, w/80)
	side := math.Min(w, h) / 4
	if w > h {
		side = w / 4
	}
	side = math.Min(side, h/2-margin)
	var best box
	bestD := -1.0
	for _, c := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x0 := margin + c[0]*(w-2*margin-side)
		y0 := margin + c[1]*(h-2*margin-side)
		b := box{x0, y0, x0 + side, y0 + side}
		if b.overlaps(grow(l.Mark, margin)) {
			continue
		}
		d := math.Hypot((b.X0+b.X1)/2-cx, (b.Y0+b.Y1)/2-cy)
		if d > bestD {
			best, bestD = b, d
		}
	}
	if bestD < 0 {
		return l, errors.New("the picture is too small for a zoomed-in box beside the mark")
	}
	l.Inset = best
	zoom := 3.0
	src := side / zoom
	if m.Track {
		src = math.Max(src, 1.6*math.Max(l.Mark.X1-l.Mark.X0, l.Mark.Y1-l.Mark.Y0))
	}
	sx0 := math.Max(0, math.Min(w-src, cx-src/2))
	sy0 := math.Max(0, math.Min(h-src, cy-src/2))
	l.Source = box{sx0, sy0, sx0 + src, sy0 + src}
	ix, iy := (l.Inset.X0+l.Inset.X1)/2, (l.Inset.Y0+l.Inset.Y1)/2
	nx, ny := l.Inset.X0, l.Inset.Y0
	if cx > ix {
		nx = l.Inset.X1
	}
	if cy > iy {
		ny = l.Inset.Y1
	}
	other := func(v, a, b float64) float64 {
		if v == a {
			return b
		}
		return a
	}
	l.Lines = [2][2]float64{{other(nx, l.Inset.X0, l.Inset.X1), ny}, {nx, other(ny, l.Inset.Y0, l.Inset.Y1)}}
	gap := l.R * 0.6
	th := l.TextPx * 1.25
	place := func(width float64, above bool) box {
		x0 := math.Max(margin, math.Min(w-margin-width, cx-width/2))
		if above {
			return box{x0, l.Mark.Y0 - gap - th, x0 + width, l.Mark.Y0 - gap}
		}
		return box{x0, l.Mark.Y1 + gap, x0 + width, l.Mark.Y1 + gap + th}
	}
	ok := func(b box) bool {
		return b.inside(w, h) && !b.overlaps(l.Mark) && !b.overlaps(grow(l.Inset, margin/2))
	}
	stack := func(width float64, above bool) box {
		b := place(width, above)
		for i := 0; i < 4 && !ok(b); i++ {
			if above {
				b.Y0, b.Y1 = b.Y0-th, b.Y1-th
			} else {
				b.Y0, b.Y1 = b.Y0+th, b.Y1+th
			}
		}
		return b
	}
	l.Label, l.Name = place(labelW, true), place(nameW, false)
	if !ok(l.Label) || !ok(l.Name) {
		l.Label, l.Name = place(labelW, false), place(nameW, true)
		l.Shifted = true
	}
	if !ok(l.Label) || !ok(l.Name) || l.Label.overlaps(l.Name) {
		above := cy > h/2
		l.Label = stack(labelW, above)
		l.Name = stack(nameW, above)
		if above {
			l.Label.Y0, l.Label.Y1 = l.Name.Y0-th, l.Name.Y0
		} else {
			l.Name.Y0, l.Name.Y1 = l.Label.Y1, l.Label.Y1+th
		}
		l.Shifted = true
	}
	if !ok(l.Label) || !ok(l.Name) || l.Label.overlaps(l.Name) {
		return l, errors.New("no room for the labels beside the mark")
	}
	return l, nil
}

func grow(b box, d float64) box { return box{b.X0 - d, b.Y0 - d, b.X1 + d, b.Y1 + d} }

//nolint:gochecknoglobals // the mark colour
var gold = color.RGBA{232, 184, 64, 255}

// maxShareWidth is the widest share picture made.
const maxShareWidth = 2000

// SharePicture draws the share picture: the colour picture scaled to at
// most maxShareWidth, the mark, the label above it, the name below it and
// a zoomed-in box with connector lines, as a JPEG.
func SharePicture(src image.Image, m Mark, label, name string) ([]byte, error) {
	b := src.Bounds()
	scale := math.Min(1, maxShareWidth/float64(b.Dx()))
	w, h := int(math.Round(float64(b.Dx())*scale)), int(math.Round(float64(b.Dy())*scale))
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, w, h))}
	xdraw.CatmullRom.Scale(c.img, c.img.Bounds(), src, b, draw.Src, nil)
	m.X, m.Y, m.X1, m.Y1 = m.X*scale, m.Y*scale, m.X1*scale, m.Y1*scale
	text := math.Max(14, float64(w)/55)
	l, err := layoutFor(float64(w), float64(h), m, textWidth(label, text, true), textWidth(name, text, true))
	if err != nil {
		return nil, err
	}
	zoom := image.NewRGBA(image.Rect(0, 0, int(l.Inset.X1-l.Inset.X0), int(l.Inset.Y1-l.Inset.Y0)))
	sr := image.Rect(int(l.Source.X0), int(l.Source.Y0), int(l.Source.X1), int(l.Source.Y1))
	xdraw.CatmullRom.Scale(zoom, zoom.Bounds(), c.img, sr, draw.Src, nil)
	k := float64(zoom.Bounds().Dx()) / float64(sr.Dx())
	zc := &canvas{img: zoom}
	cx, cy := m.centre()
	if m.Track {
		zc.line((m.X-float64(sr.Min.X))*k, (m.Y-float64(sr.Min.Y))*k, (m.X1-float64(sr.Min.X))*k, (m.Y1-float64(sr.Min.Y))*k, 3, gold)
	} else {
		zc.ring((cx-float64(sr.Min.X))*k, (cy-float64(sr.Min.Y))*k, math.Max(10, l.R*1.6), 2.5, gold)
	}
	zc.halo(l.TextPx*0.6, float64(zoom.Bounds().Dy())-l.TextPx*0.6, "zoomed in", l.TextPx*0.75, true, 0)
	draw.Draw(c.img, image.Rect(int(l.Inset.X0), int(l.Inset.Y0), int(l.Inset.X1), int(l.Inset.Y1)), zoom, image.Point{}, draw.Src)
	white := color.RGBA{240, 240, 240, 220}
	c.rect(l.Inset.X0, l.Inset.Y0, l.Inset.X1, l.Inset.Y1, 1.5, white)
	for _, p := range l.Lines {
		ex, ey := towards(cx, cy, p[0], p[1], l.R)
		c.line(p[0], p[1], ex, ey, 1, white)
	}
	if m.Track {
		c.line(m.X, m.Y, m.X1, m.Y1, 3, gold)
	} else {
		c.ring(cx, cy, l.R, 2.5, gold)
	}
	c.halo(l.Label.X0, l.Label.Y1-l.TextPx*0.25, label, l.TextPx, true, 0)
	c.halo(l.Name.X0, l.Name.Y1-l.TextPx*0.25, name, l.TextPx, true, 0)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, c.img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// towards is the point r from x, y toward px, py.
func towards(x, y, px, py, r float64) (float64, float64) {
	d := math.Hypot(px-x, py-y)
	if d == 0 {
		return x, y
	}
	return x + (px-x)/d*r, y + (py-y)/d*r
}
