package dustcheck

import (
	"math"
	"slices"
)

// Blob is a shadow (or brightening) in a map, in sensor pixels.
type Blob struct {
	X, Y int
	// Diameter of a disc of the blob's area.
	Diameter int
	// Peak and Mean are the deepest and mean deviation, in percent, signed:
	// negative for a shadow.
	Peak float64
	Mean float64
	// Edge is how much the blob stands out from a ring just outside it
	// (1: entirely), and Sharp how steep its rim is for its depth.
	Edge  float64
	Sharp float64
	// Disc is whether it has the sharp, contained rim of a dust shadow
	// rather than a soft illumination change.
	Disc bool
}

// Blobs finds shadows (sign -1) or brightenings (+1) in a smoothed map:
// seeds beyond thr grow down to half of it, so one disc is one blob, and
// those at least MinDiameter across are kept. edgeMin is the Edge a disc
// needs.
func Blobs(s Smoothed, thr float64, sign int, edgeMin float64) []Blob {
	w, h := s.W, s.H
	v := make([]float64, len(s.Pix))
	for i, p := range s.Pix {
		v[i] = float64(sign) * float64(p)
	}
	lab := make([]int32, len(v))
	grow := 0.5 * thr
	var out []Blob
	var grad []float64
	next := int32(0)
	stack := make([]int, 0, 1024)
	var members []int
	for start := range v {
		if lab[start] != 0 || !s.Mask[start] || !(v[start] > grow) {
			continue
		}
		next++
		lab[start] = next
		stack = append(stack[:0], start)
		members = members[:0]
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			members = append(members, i)
			x, y := i%w, i/w
			for _, n := range [4][3]int{{x - 1, y, i - 1}, {x + 1, y, i + 1}, {x, y - 1, i - w}, {x, y + 1, i + w}} {
				if n[0] < 0 || n[0] >= w || n[1] < 0 || n[1] >= h {
					continue
				}
				j := n[2]
				if lab[j] == 0 && s.Mask[j] && v[j] > grow {
					lab[j] = next
					stack = append(stack, j)
				}
			}
		}
		peak := math.Inf(-1)
		sum, cx, cy := 0.0, 0.0, 0.0
		for _, i := range members {
			peak = max(peak, v[i])
			sum += v[i]
			cx += float64(i % w)
			cy += float64(i / w)
		}
		if peak < thr {
			continue
		}
		area := float64(len(members))
		d := 2 * math.Sqrt(area/math.Pi)
		if d < MinDiameter {
			continue
		}
		cx /= area
		cy /= area
		inside := sum / area
		if grad == nil {
			grad = gradient(v, w, h)
		}
		b := describe(v, grad, s.Mask, w, h, cx, cy, d, inside)
		b.X = int(math.Round(cx * BinFactor))
		b.Y = int(math.Round(cy * BinFactor))
		b.Diameter = int(math.Round(d * BinFactor))
		b.Peak = round3(float64(sign) * peak * 100)
		b.Mean = round3(float64(sign) * inside * 100)
		b.Disc = b.Edge >= edgeMin && b.Sharp >= SharpMin
		b.Edge = math.Round(b.Edge*100) / 100
		b.Sharp = math.Round(b.Sharp*100) / 100
		out = append(out, b)
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// describe measures a blob's Edge and Sharp around its centroid.
func describe(v, grad []float64, mask []bool, w, h int, cx, cy, d, inside float64) Blob {
	r := d / 2
	x0, x1 := max(0, int(cx-1.7*r)), min(w-1, int(cx+1.7*r)+1)
	y0, y1 := max(0, int(cy-1.7*r)), min(h-1, int(cy+1.7*r)+1)
	var ring []float32
	var rim []float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			i := y*w + x
			if !mask[i] {
				continue
			}
			rr := math.Hypot(float64(y)-cy, float64(x)-cx)
			if rr > 1.15*r && rr < 1.6*r {
				ring = append(ring, float32(v[i]))
			}
			if rr > 0.7*r && rr < 1.3*r {
				rim = append(rim, grad[i])
			}
		}
	}
	outside := 0.0
	if len(ring) > 0 {
		outside = float64(median(ring))
	}
	var b Blob
	if inside > 0 {
		b.Edge = (inside - outside) / inside
	}
	top := 0.0
	if len(rim) > 0 {
		slices.Sort(rim)
		k := max(1, int(float64(len(rim))*0.2))
		top = median64(rim[len(rim)-k:])
	}
	if inside > 0 {
		b.Sharp = top * 10 / inside
	}
	return b
}

func median64(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// gradient is |∇v| with numpy's central differences, one-sided at edges.
func gradient(v []float64, w, h int) []float64 {
	g := make([]float64, len(v))
	for y := range h {
		for x := range w {
			i := y*w + x
			var gx, gy float64
			switch {
			case w < 2:
			case x == 0:
				gx = v[i+1] - v[i]
			case x == w-1:
				gx = v[i] - v[i-1]
			default:
				gx = (v[i+1] - v[i-1]) / 2
			}
			switch {
			case h < 2:
			case y == 0:
				gy = v[i+w] - v[i]
			case y == h-1:
				gy = v[i] - v[i-w]
			default:
				gy = (v[i+w] - v[i-w]) / 2
			}
			g[i] = math.Hypot(gx, gy)
		}
	}
	return g
}
