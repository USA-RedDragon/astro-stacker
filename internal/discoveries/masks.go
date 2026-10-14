package discoveries

import "math"

// edgeMask marks pixels within r pixels (in x or y) of a pixel with no
// data, and within borderPixels of the image border.
func edgeMask(valid []bool, w, h, r int) []bool {
	bad := make([]int32, w*h)
	row := make([]int32, w+1)
	for y := range h {
		row[0] = 0
		for x := range w {
			v := int32(0)
			if !valid[y*w+x] {
				v = 1
			}
			row[x+1] = row[x] + v
		}
		for x := range w {
			bad[y*w+x] = row[min(x+r+1, w)] - row[max(x-r, 0)]
		}
	}
	out := make([]bool, w*h)
	col := make([]int32, h+1)
	for x := range w {
		col[0] = 0
		for y := range h {
			v := int32(0)
			if bad[y*w+x] > 0 {
				v = 1
			}
			col[y+1] = col[y] + v
		}
		for y := range h {
			out[y*w+x] = col[min(y+r+1, h)]-col[max(y-r, 0)] > 0 || x < borderPixels || y < borderPixels || x >= w-borderPixels || y >= h-borderPixels
		}
	}
	return out
}

const borderPixels = 16

const (
	brightMaskG    = 14.0
	brightMaskBase = 6.0
	brightMaskMax  = 250.0
)

// brightRadius is how far, in pixels, a star of Gaia G mag spoils the
// difference image.
func brightRadius(g float64) float64 {
	return math.Max(brightMaskBase, math.Min(brightMaskBase*math.Pow(10, 0.2*(brightMaskG-g)), brightMaskMax))
}

// brightMask marks discs around Gaia stars brighter than G 14, growing with
// brightness.
func brightMask(stars []Star, wcs *WCS, w, h int) []bool {
	m := make([]bool, w*h)
	for _, s := range stars {
		if s.G >= brightMaskG {
			continue
		}
		x, y, ok := wcs.PixelOf(s.RA, s.Dec)
		if !ok {
			continue
		}
		r := brightRadius(s.G)
		if x < -r || y < -r || x > float64(w)+r || y > float64(h)+r {
			continue
		}
		for yy := max(0, int(y-r)); yy <= min(h-1, int(y+r+1)); yy++ {
			for xx := max(0, int(x-r)); xx <= min(w-1, int(x+r+1)); xx++ {
				if (float64(xx)-x)*(float64(xx)-x)+(float64(yy)-y)*(float64(yy)-y) < r*r {
					m[yy*w+xx] = true
				}
			}
		}
	}
	return m
}
