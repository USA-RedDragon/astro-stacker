// Package publicframe renders the frame the public site shows of the newest
// accepted light: small, stretched, 8-bit and lossy, so it is no use as
// data, and carrying a watermark too faint to see in one frame that appears
// when frames are stacked.
//
// The watermark is fixed on the sky, not on the frame. Lights are rendered
// from their registered copies, which the stacker has already resampled
// onto the target's reference grid (every filter of a target registers to
// the same reference), and the watermark is defined on that grid. Dithers,
// meridian flips and rotator angles are all taken out by registration, so
// a thief who registers the public frames on their stars stacks the
// watermark coherently, while the sky noise averages down.
package publicframe

import "math"

// The watermark is tiled text in a 5×9 bitmap font. Sizes are in font
// pixels; a font pixel is the reference width over fontPixelsAcross, so the
// letters are the same size in the public frame whatever the camera.
const (
	text    = "astro.garden"
	glyphW  = 5
	glyphH  = 9
	advance = 6  // glyph width plus spacing
	tileW   = 90 // text is 12×6 = 72 font pixels; 18 of gap
	rowH    = 14 // glyph plus gap between rows
	tileH   = 2 * rowH
	// supersample is the tile's samples per font pixel.
	supersample = 8
)

// The letters' size and weight, tuned on real subs (Leo Triplet RGB, Orion
// H-a): bold and soft enough to come through 8× binning, JPEG at quality 60
// and a thief's registration, and still read in a stack. A font pixel is
// about 6 output pixels, so the text is about 55 px tall with its
// descender and a word about 450 px wide: smaller than that, the rows read
// as bands in one blurred frame; larger, nebulosity (strongest at large
// scales) hides them. Variables only so the local demo can try others.
var (
	fontPixelsAcross = 130.0
	// strokeGrow thickens the strokes, in samples each side (a stroke is
	// 1¾ font pixels); blurSigma softens the letters' edges, in font
	// pixels, so they hold little high-frequency energy for JPEG to
	// quantize away.
	strokeGrow = 3
	blurSigma  = 0.5
)

// glyphs are drawn with '#' on rows 0-8: ascenders from row 0, the x-height
// from row 2, the baseline under row 6, descenders to row 8.
var glyphs = map[rune][glyphH]string{
	'a': {".....", ".....", ".###.", "....#", ".####", "#...#", ".####", ".....", "....."},
	's': {".....", ".....", ".####", "#....", ".###.", "....#", "####.", ".....", "....."},
	't': {".#...", ".#...", "####.", ".#...", ".#...", ".#..#", "..##.", ".....", "....."},
	'r': {".....", ".....", "#.##.", "##..#", "#....", "#....", "#....", ".....", "....."},
	'o': {".....", ".....", ".###.", "#...#", "#...#", "#...#", ".###.", ".....", "....."},
	'.': {".....", ".....", ".....", ".....", ".....", ".##..", ".##..", ".....", "....."},
	'g': {".....", ".....", ".####", "#...#", "#...#", ".####", "....#", "....#", ".###."},
	'd': {"....#", "....#", ".####", "#...#", "#...#", "#...#", ".####", ".....", "....."},
	'e': {".....", ".....", ".###.", "#...#", "#####", "#....", ".###.", ".....", "....."},
	'n': {".....", ".....", "#.##.", "##..#", "#...#", "#...#", "#...#", ".....", "....."},
}

// Pattern is the watermark on a reference grid: 1 in the letters' cores,
// falling softly to 0 around them.
type Pattern struct {
	// fontPx is the size of a font pixel in reference pixels.
	fontPx float64
	tile   []float32 // tileW×tileH font pixels, supersampled
	tw, th int
}

// NewPattern lays the watermark on a reference grid refW pixels wide.
func NewPattern(refW int) *Pattern {
	tw, th := tileW*supersample, tileH*supersample
	tile := make([]float32, tw*th)
	// Two rows per tile, the second shifted half a tile, like bricks, so
	// the tile repeats exactly and a crop of any shape holds whole words.
	for row := range 2 {
		x0 := 4 + row*tileW/2
		y0 := row*rowH + 2
		for i, r := range text {
			g := glyphs[r]
			for gy := range glyphH {
				for gx := range glyphW {
					if g[gy][gx] != '#' {
						continue
					}
					fx, fy := x0+i*advance+gx, y0+gy
					for sy := -strokeGrow; sy < supersample+strokeGrow; sy++ {
						for sx := -strokeGrow; sx < supersample+strokeGrow; sx++ {
							x := ((fx*supersample+sx)%tw + tw) % tw
							y := ((fy*supersample+sy)%th + th) % th
							tile[y*tw+x] = 1
						}
					}
				}
			}
		}
	}
	tile = blurWrap(tile, tw, th, blurSigma*supersample)
	var peak float32
	for _, v := range tile {
		peak = max(peak, v)
	}
	if peak > 0 {
		for i := range tile {
			tile[i] /= peak
		}
	}
	return &Pattern{fontPx: float64(refW) / fontPixelsAcross, tile: tile, tw: tw, th: th}
}

// At is the watermark at reference pixel (x, y).
func (p *Pattern) At(x, y float64) float64 {
	sx := x / p.fontPx * supersample
	sy := y / p.fontPx * supersample
	x0, y0 := math.Floor(sx), math.Floor(sy)
	fx, fy := sx-x0, sy-y0
	ix, iy := int(x0), int(y0)
	at := func(x, y int) float64 {
		x = (x%p.tw + p.tw) % p.tw
		y = (y%p.th + p.th) % p.th
		return float64(p.tile[y*p.tw+x])
	}
	return (1-fy)*((1-fx)*at(ix, iy)+fx*at(ix+1, iy)) + fy*((1-fx)*at(ix, iy+1)+fx*at(ix+1, iy+1))
}

// blurWrap is a separable Gaussian blur that wraps around the edges.
func blurWrap(in []float32, w, h int, sigma float64) []float32 {
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	var sum float64
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, len(in))
	for y := range h {
		for x := range w {
			var s float64
			for i, kv := range k {
				s += kv * float64(in[y*w+((x+i-r)%w+w)%w])
			}
			tmp[y*w+x] = float32(s)
		}
	}
	out := make([]float32, len(in))
	for y := range h {
		for x := range w {
			var s float64
			for i, kv := range k {
				s += kv * float64(tmp[((y+i-r)%h+h)%h*w+x])
			}
			out[y*w+x] = float32(s)
		}
	}
	return out
}
