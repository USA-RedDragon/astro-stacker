// Package preview renders an auto-stretched JPEG of a linear frame, the same
// way PixInsight's ScreenTransferFunction auto-stretch does, so a sub looks
// the way it would when opened in PixInsight.
package preview

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"slices"

	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
)

// PixInsight's STF auto-stretch defaults.
const (
	ShadowsClip      = -2.8
	TargetBackground = 0.25
	madToSigma       = 1.4826
)

// Options controls preview size and quality.
type Options struct {
	MaxWidth int
	Quality  int
}

func DefaultOptions() Options { return Options{MaxWidth: 1600, Quality: 85} }

// Render bins im to at most MaxWidth pixels wide, stretches each channel, and
// encodes a JPEG.
func Render(im *imagedata.Image, opts Options) ([]byte, error) {
	if opts.MaxWidth <= 0 {
		opts = DefaultOptions()
	}
	factor := max(1, int(math.Ceil(float64(im.W)/float64(opts.MaxWidth))))
	binned := Bin(im, factor)

	planes := make([][]float32, binned.C)
	for c := range binned.C {
		planes[c] = Stretch(binned.Plane(c))
	}

	var img image.Image
	if binned.C >= 3 {
		rgba := image.NewRGBA(image.Rect(0, 0, binned.W, binned.H))
		for i := range binned.W * binned.H {
			rgba.Pix[4*i] = to8(planes[0][i])
			rgba.Pix[4*i+1] = to8(planes[1][i])
			rgba.Pix[4*i+2] = to8(planes[2][i])
			rgba.Pix[4*i+3] = 0xff
		}
		img = rgba
	} else {
		gray := image.NewGray(image.Rect(0, 0, binned.W, binned.H))
		for i := range binned.W * binned.H {
			gray.Pix[i] = to8(planes[0][i])
		}
		img = gray
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: opts.Quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func to8(v float32) uint8 {
	return color.Gray{Y: uint8(math.Round(float64(min(max(v, 0), 1)) * 255))}.Y
}

// Bin averages factor×factor blocks, dropping partial blocks at the edges.
func Bin(im *imagedata.Image, factor int) *imagedata.Image {
	if factor <= 1 {
		return im
	}
	w, h := im.W/factor, im.H/factor
	out := &imagedata.Image{W: w, H: h, C: im.C, Data: make([]float32, w*h*im.C)}
	inv := 1 / float32(factor*factor)
	for c := range im.C {
		src := im.Plane(c)
		dst := out.Plane(c)
		for y := range h {
			for x := range w {
				var sum float32
				for dy := range factor {
					row := (y*factor + dy) * im.W
					for dx := range factor {
						sum += src[row+x*factor+dx]
					}
				}
				dst[y*w+x] = sum * inv
			}
		}
	}
	return out
}

// Stretch applies the STF auto-stretch to one channel: shadows clipped at
// median + ShadowsClip·σ (σ from the MAD), and a midtones transfer that puts
// the median at TargetBackground.
func Stretch(p []float32) []float32 {
	c0, m := StretchParams(p)
	return StretchWith(p, c0, m)
}

// StretchParams are the STF auto-stretch's shadows clip and midtones
// balance for one channel.
func StretchParams(p []float32) (c0, m float64) {
	med, madn := medianMAD(p)
	c0 = math.Max(0, math.Min(1, med+ShadowsClip*madn))
	return c0, MTF(TargetBackground, med-c0)
}

// StretchWith stretches with a shadows clip and midtones balance.
func StretchWith(p []float32, c0, m float64) []float32 {
	out := make([]float32, len(p))
	span := 1 - c0
	for i, v := range p {
		x := (float64(v) - c0) / span
		out[i] = float32(MTF(m, math.Max(0, math.Min(1, x))))
	}
	return out
}

// MTF is PixInsight's midtones transfer function.
func MTF(m, x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	case x == m:
		return 0.5
	}
	return (m - 1) * x / ((2*m-1)*x - m)
}

func medianMAD(p []float32) (float64, float64) {
	// A strided sample keeps this fast on large frames without changing the
	// statistics meaningfully.
	stride := max(1, len(p)/500_000)
	s := make([]float32, 0, len(p)/stride+1)
	for i := 0; i < len(p); i += stride {
		s = append(s, p[i])
	}
	slices.Sort(s)
	med := float64(s[len(s)/2])
	for i, v := range s {
		s[i] = float32(math.Abs(float64(v) - med))
	}
	slices.Sort(s)
	return med, float64(s[len(s)/2]) * madToSigma
}
