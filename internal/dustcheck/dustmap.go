// Package dustcheck finds dust shadows that came, went or moved between
// master flats, and shadows in a night's lights that their flats do not
// hold. It works on 4×4 binned maps of the fractional deviation from a wide
// background, so a shadow 0.5 % deep reads -0.005.
package dustcheck

import (
	"math"
	"slices"
)

// Tuning found on the flat history from 2025-02 to 2026-10.
const (
	// BinFactor is how many sensor pixels a map pixel is across.
	BinFactor = 4
	// BackgroundSize is the background median's width in map pixels.
	BackgroundSize = 301
	bgStep         = 8
	// Border is the map pixels along each edge left out of detection.
	Border = 60
	// Smooth is the Gaussian sigma, in map pixels, a map is smoothed with
	// before blobs are found.
	Smooth = 4.0
	// MinDiameter is the smallest blob, in map pixels, that counts.
	MinDiameter = 30
	// DiffMin is the shallowest change between two flats that counts.
	DiffMin = 0.0025
	// KSigma is how many noise sigmas a change must stand out by.
	KSigma = 5.0
	// SharpMin and EdgeMin tell a dust disc from a diffuse light change.
	SharpMin = 0.4
	EdgeMin  = 0.5
	// RotationTolerance is how far apart in degrees two flats' rotator
	// angles may be and still count as the same angle.
	RotationTolerance = 1.5
	shadowMask        = -0.0015
)

// DustMap is a flat's dust: the 4×4 binned flat divided by a wide
// background, less one. Dark shadows found in one pass are masked out of
// the next pass's background, so a big shadow does not pull the background
// down and leave a bright halo around itself. It is worked bottom row first,
// as FITS stores it and as the tuning was done: the 8×8 background grid
// drops the rows that don't fill a block from the bottom.
func DustMap(pix []float32, w, h int) Map {
	x := Bin(pix, w, h, BinFactor)
	flipRows(x)
	r := dustMapBinned(x)
	flipRows(r)
	return r
}

func flipRows(m Map) {
	for top, bottom := 0, m.H-1; top < bottom; top, bottom = top+1, bottom-1 {
		a := m.Pix[top*m.W : (top+1)*m.W]
		b := m.Pix[bottom*m.W : (bottom+1)*m.W]
		for i := range a {
			a[i], b[i] = b[i], a[i]
		}
	}
}

func dustMapBinned(x Map) Map {
	normalize(x)
	bg := background(x, nil)
	r := NewMap(x.W, x.H)
	for range 2 {
		ratio(x, bg, r)
		g := Gaussian(r, 3)
		mask := make([]bool, len(g.Pix))
		for i, v := range g.Pix {
			mask[i] = v < shadowMask
		}
		mask = dilate(mask, x.W, x.H, 6)
		const e = 50
		for y := range x.H {
			for xx := range x.W {
				if y < e || y >= x.H-e || xx < e || xx >= x.W-e {
					mask[y*x.W+xx] = false
				}
			}
		}
		bg = background(x, mask)
	}
	ratio(x, bg, r)
	return r
}

func normalize(x Map) {
	med := median(slices.Clone(x.Pix))
	for i := range x.Pix {
		x.Pix[i] /= med
	}
}

func ratio(x, bg, out Map) {
	for i := range x.Pix {
		out.Pix[i] = x.Pix[i]/bg.Pix[i] - 1
	}
}

// background is x's wide median, leaving out masked pixels: an 8×8 binned
// copy, with blocks mostly masked filled from their neighbours, median
// filtered, zoomed back and smoothed.
func background(x Map, mask []bool) Map {
	var small Map
	if mask == nil {
		small = Bin(x.Pix, x.W, x.H, bgStep)
	} else {
		masked := make([]float32, len(x.Pix))
		valid := make([]float32, len(x.Pix))
		for i, v := range x.Pix {
			if !mask[i] {
				masked[i], valid[i] = v, 1
			}
		}
		sum := Bin(masked, x.W, x.H, bgStep)
		frac := Bin(valid, x.W, x.H, bgStep)
		small = NewMap(sum.W, sum.H)
		for i := range small.Pix {
			if frac.Pix[i] > 0.3 {
				small.Pix[i] = sum.Pix[i] / max(frac.Pix[i], 1e-3)
			} else {
				small.Pix[i] = float32(math.NaN())
			}
		}
		fillNaN(small)
	}
	bg := MedianFilter(small, (BackgroundSize/bgStep)|1)
	return Gaussian(Zoom(bg, x.W, x.H), 2)
}

// Smoothed is a map smoothed for detection, with its median taken out and
// Border zeroed, and Mask marking the pixels inside Border.
type Smoothed struct {
	Map
	Mask []bool
}

// Prepare smooths a dust map for detection.
func Prepare(r Map) Smoothed {
	s := Gaussian(r, Smooth)
	mask := interior(s.W, s.H)
	var in []float32
	for i, ok := range mask {
		if ok {
			in = append(in, s.Pix[i])
		}
	}
	med := median(in)
	for i, ok := range mask {
		if ok {
			s.Pix[i] -= med
		} else {
			s.Pix[i] = 0
		}
	}
	return Smoothed{Map: s, Mask: mask}
}

func interior(w, h int) []bool {
	mask := make([]bool, w*h)
	for y := Border; y < h-Border; y++ {
		for x := Border; x < w-Border; x++ {
			mask[y*w+x] = true
		}
	}
	return mask
}

// Sigma is the robust noise of a smoothed map inside its mask.
func Sigma(s Smoothed) float64 {
	var v []float32
	for i, ok := range s.Mask {
		if ok {
			v = append(v, s.Pix[i])
		}
	}
	m := median(slices.Clone(v))
	for i := range v {
		v[i] = float32(math.Abs(float64(v[i] - m)))
	}
	return 1.4826 * float64(median(v))
}

// Diff is b - a, keeping b's mask.
func Diff(a, b Smoothed) Smoothed {
	d := NewMap(b.W, b.H)
	for i := range d.Pix {
		d.Pix[i] = b.Pix[i] - a.Pix[i]
	}
	return Smoothed{Map: d, Mask: b.Mask}
}
