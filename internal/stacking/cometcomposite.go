package stacking

import (
	"math"
)

// Stacked on the comet, what the stars' subtraction leaves of them trails
// as faint dashes, each filter's along its own stretch of the comet's path,
// so a colour cover shows them in colour. The comet master is made as
// comet pictures usually are: the stars and sky stacked on the stars, with
// the comet taken out of every sub, and the starless comet on top.

// compositeBlock is the block size, in pixels, the comet's extent is
// measured on.
const compositeBlock = 8

// compositeNucleus is the radius, in pixels, round the nucleus where the
// comet keeps every pixel; beyond it the comet is median-filtered, which
// takes out what is left of the stars' trails.
const compositeNucleus = 12

// cometSignal is the comet alone, sky-free, from a starless comet stack on
// the reference sub's pixels, inside cometMask; box bounds it. Pixels
// inside that the stack has no data for, a nucleus saturated in every sub,
// hold the smallest float32. ok is false when the nucleus doesn't stand
// out.
func cometSignal(starless []float32, w, h int, nucleus [2]float64) (signal []float32, box Rect, ok bool) {
	mask, sky, ok := cometMask(starless, w, h, nucleus)
	if !ok {
		return nil, Rect{}, false
	}
	box = maskBox(mask, w, h)
	sky = localSky(starless, mask, w, h, box, sky)
	filtered := medianFilter(crop(starless, w, box), box.W, box.H, 2)
	signal = make([]float32, w*h)
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			i := y*w + x
			m := mask[i]
			if m <= 0 {
				continue
			}
			if starless[i] == 0 {
				signal[i] = math.SmallestNonzeroFloat32
				continue
			}
			v := filtered[(y-box.Y)*box.W+x-box.X]
			if r := math.Hypot(float64(x)-nucleus[0], float64(y)-nucleus[1]); r < 2*compositeNucleus {
				f := float32(max(0, min(1, (r-compositeNucleus)/compositeNucleus)))
				v = f*v + (1-f)*starless[i]
			}
			signal[i] = m * (v - float32(sky))
		}
	}
	return signal, box, true
}

// cometMask weighs each pixel by how much it belongs to the comet: 1 on the
// blocks connected to the nucleus that stand 3σ above the sky, grown and
// feathered to 0. sky is the stack's sky level.
func cometMask(starless []float32, w, h int, nucleus [2]float64) (mask []float32, sky float64, ok bool) {
	const s = compositeBlock
	blocks, gw, gh := blockMedians(starless, w, h, s)
	vals := make([]float64, 0, len(blocks))
	grid := make([]float32, len(blocks))
	for i, v := range blocks {
		if !math.IsNaN(v) {
			vals = append(vals, v)
			grid[i] = float32(v)
		}
	}
	if len(vals) == 0 {
		return nil, 0, false
	}
	sky = median(vals)
	smooth := medianFilter(grid, gw, gh, 1)
	dev := make([]float64, 0, len(smooth))
	for _, v := range smooth {
		if v != 0 {
			dev = append(dev, math.Abs(float64(v)-sky))
		}
	}
	sigma := median(dev) * madToSigma
	nx, ny := int(nucleus[0])/s, int(nucleus[1])/s
	if nx < 0 || ny < 0 || nx >= gw || ny >= gh || !(sigma > 0) {
		return nil, 0, false
	}
	above := func(i int) bool { return smooth[i] != 0 && float64(smooth[i])-sky > 3*sigma }
	if !above(ny*gw + nx) {
		return nil, 0, false
	}
	weight := make([]float64, len(smooth))
	queue := []int{ny*gw + nx}
	weight[queue[0]] = 1
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%gw, i/gw
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			xx, yy := x+d[0], y+d[1]
			if xx < 0 || yy < 0 || xx >= gw || yy >= gh {
				continue
			}
			if j := yy*gw + xx; weight[j] == 0 && above(j) {
				weight[j] = 1
				queue = append(queue, j)
			}
		}
	}
	const grow, feather = 3, 3
	weight = gaussGrid(dilateGrid(weight, gw, gh, grow+3*feather), gw, gh, feather)
	return upsampleGrid(weight, gw, gh, s, w, h), sky, true
}

// localSky is the sky round the comet: the median of the pixels outside
// mask within a quarter of box's size of it, or sky when too few. A comet
// stack's sky strays from its median by a fraction of its noise over the
// frame, and taken with the comet that would lift or sink its whole region.
func localSky(starless, mask []float32, w, h int, box Rect, sky float64) float64 {
	mx, my := max(16, box.W/4), max(16, box.H/4)
	x0, y0 := max(0, box.X-mx), max(0, box.Y-my)
	x1, y1 := min(w, box.X+box.W+mx), min(h, box.Y+box.H+my)
	stride := max(1, int(math.Sqrt(float64((x1-x0)*(y1-y0))/200_000)))
	var vals []float64
	for y := y0; y < y1; y += stride {
		for x := x0; x < x1; x += stride {
			if i := y*w + x; mask[i] == 0 && starless[i] != 0 {
				vals = append(vals, float64(starless[i]))
			}
		}
	}
	if len(vals) < 1000 {
		return sky
	}
	return median(vals)
}

// maskBox bounds the pixels of mask above 0.
func maskBox(mask []float32, w, h int) Rect {
	x0, y0, x1, y1 := w, h, -1, -1
	for y := range h {
		for x := range w {
			if mask[y*w+x] > 0 {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	return Rect{X: x0, Y: y0, W: x1 - x0 + 1, H: y1 - y0 + 1}
}

// sample is p at (x, y), bilinear, 0 outside.
func sample(p []float32, w, h int, x, y float64) float32 {
	if x < 0 || y < 0 || x > float64(w-1) || y > float64(h-1) {
		return 0
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	top := p[y0*w+x0]*(1-fx) + p[y0*w+x1]*fx
	bot := p[y1*w+x0]*(1-fx) + p[y1*w+x1]*fx
	return top*(1-fy) + bot*fy
}

// subtractComet takes scale × signal, held where the comet is on the
// reference sub, out of a star-aligned sub in which it is shift away.
// Empty and saturated pixels stay as they are.
func subtractComet(sub []float32, w, h int, signal []float32, box Rect, shift [2]float64, scale float64, sat float32) {
	k := float32(scale)
	x0, y0 := max(0, int(math.Floor(float64(box.X)-shift[0]))-1), max(0, int(math.Floor(float64(box.Y)-shift[1]))-1)
	x1, y1 := min(w, int(math.Ceil(float64(box.X+box.W)-shift[0]))+1), min(h, int(math.Ceil(float64(box.Y+box.H)-shift[1]))+1)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*w + x
			v := sub[i]
			if v == 0 || v >= sat {
				continue
			}
			c := sample(signal, w, h, float64(x)+shift[0], float64(y)+shift[1])
			if c == 0 {
				continue
			}
			if r := v - k*c; r != 0 {
				sub[i] = r
			} else {
				sub[i] = 1e-9
			}
		}
	}
}
