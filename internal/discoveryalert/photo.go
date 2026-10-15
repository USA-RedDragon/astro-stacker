package discoveryalert

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// CutoutReader reads a candidate's stored cutouts.
type CutoutReader interface {
	Cutout(ctx context.Context, key string) ([]byte, error)
}

const (
	panelSide = 300
	gap       = 6
	labelBand = 22
	tick      = 18
	tickGap   = 24
)

// Photo puts a candidate's night, reference and difference cutouts side by
// side with a label over each and ticks pointing at the centre, as a JPEG.
// A missing or unreadable cutout leaves its panel grey with a note.
func Photo(ctx context.Context, r CutoutReader, key string) []byte {
	w := 3*panelSide + 2*gap
	h := labelBand + panelSide
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	for i, p := range [][2]string{{"night.png", "this night"}, {"ref.png", "reference"}, {"diff.png", "difference"}} {
		name := p[0]
		x0 := i * (panelSide + gap)
		dst := image.Rect(x0, labelBand, x0+panelSide, labelBand+panelSide)
		label(img, x0+6, 16, p[1])
		var src image.Image
		if key != "" && r != nil {
			if b, err := r.Cutout(ctx, key+name); err == nil {
				src, _ = png.Decode(bytes.NewReader(b))
			}
		}
		if src == nil {
			draw.Draw(img, dst, image.NewUniform(color.Gray{Y: 60}), image.Point{}, draw.Src)
			label(img, x0+panelSide/2-42, labelBand+panelSide/2, "no cutout")
			continue
		}
		draw.NearestNeighbor.Scale(img, dst, src, src.Bounds(), draw.Src, nil)
		ticks(img, dst)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil
	}
	return out.Bytes()
}

func label(img *image.RGBA, x, y int, s string) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func ticks(img *image.RGBA, r image.Rectangle) {
	c := color.RGBA{R: 255, G: 64, B: 64, A: 255}
	cx, cy := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	for d := tickGap; d < tickGap+tick; d++ {
		for t := -1; t <= 1; t++ {
			img.Set(cx-d, cy+t, c)
			img.Set(cx+t, cy-d, c)
		}
	}
}
