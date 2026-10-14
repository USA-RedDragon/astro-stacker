package discoveries

import (
	"math"
)

// Source is one detection: its flux-weighted centre, shape from second
// moments, and size.
type Source struct {
	X, Y  float64
	A, B  float64
	Flux  float64
	Peak  float64
	NPix  int
	Edge  bool
	Theta float64
}

// matchedNorm is the square root of the summed squares of sep's default
// 3x3 detection filter, {1 2 1, 2 4 2, 1 2 1}.
const matchedNorm = 6

// Extract finds sources in p, a background-subtracted image: pixels whose
// 3x3 matched-filtered signal is over thresh times its noise, joined into
// 8-connected groups of at least minArea pixels. rms is the per-pixel
// noise; mask, when set, excludes pixels. A source touching NaN or the
// image border is marked Edge.
func Extract(p *Plane, rms []float32, mask []bool, thresh float64, minArea int) []Source {
	matchedKernel := [3][3]float32{{1, 2, 1}, {2, 4, 2}, {1, 2, 1}}
	w, h := p.W, p.H
	above := make([]bool, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			if mask != nil && mask[i] {
				continue
			}
			v := p.Data[i]
			if isNaN32(v) {
				continue
			}
			var c float32
			for ky := -1; ky <= 1; ky++ {
				row := (y + ky) * w
				for kx := -1; kx <= 1; kx++ {
					s := p.Data[row+x+kx]
					if !isNaN32(s) {
						c += matchedKernel[ky+1][kx+1] * s
					}
				}
			}
			r := rms[i]
			if r > 0 && float64(c) > thresh*float64(r)*matchedNorm {
				above[i] = true
			}
		}
	}
	var out []Source
	stack := make([]int, 0, 256)
	pix := make([]int, 0, 256)
	for start, on := range above {
		if !on {
			continue
		}
		above[start] = false
		stack = append(stack[:0], start)
		pix = pix[:0]
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pix = append(pix, i)
			x, y := i%w, i/w
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= w || yy >= h {
						continue
					}
					j := yy*w + xx
					if above[j] {
						above[j] = false
						stack = append(stack, j)
					}
				}
			}
		}
		if len(pix) < minArea {
			continue
		}
		if s, ok := measure(p, pix); ok {
			out = append(out, s)
		}
	}
	return out
}

func measure(p *Plane, pix []int) (Source, bool) {
	w, h := p.W, p.H
	var sw, sx, sy, flux, peak float64
	edge := false
	peak = math.Inf(-1)
	for _, i := range pix {
		x, y := i%w, i/w
		if x <= 1 || y <= 1 || x >= w-2 || y >= h-2 {
			edge = true
		}
		v := float64(p.Data[i])
		flux += v
		peak = math.Max(peak, v)
		if v > 0 {
			sw += v
			sx += v * float64(x)
			sy += v * float64(y)
		}
		if !edge {
			for _, j := range [4]int{i - 1, i + 1, i - w, i + w} {
				if isNaN32(p.Data[j]) {
					edge = true
				}
			}
		}
	}
	if !(sw > 0) {
		return Source{}, false
	}
	cx, cy := sx/sw, sy/sw
	var x2, y2, xy float64
	for _, i := range pix {
		v := float64(p.Data[i])
		if v <= 0 {
			continue
		}
		dx, dy := float64(i%w)-cx, float64(i/w)-cy
		x2 += v * dx * dx
		y2 += v * dy * dy
		xy += v * dx * dy
	}
	x2, y2, xy = x2/sw, y2/sw, xy/sw
	if len(pix) == 1 || x2*y2-xy*xy < 1.0/144 {
		x2 += 1.0 / 12
		y2 += 1.0 / 12
	}
	t1 := (x2 + y2) / 2
	t2 := math.Sqrt((x2-y2)*(x2-y2)/4 + xy*xy)
	a := math.Sqrt(math.Max(t1+t2, 0))
	b := math.Sqrt(math.Max(t1-t2, 0))
	return Source{X: cx, Y: cy, A: a, B: b, Flux: flux, Peak: peak, NPix: len(pix), Edge: edge, Theta: 0.5 * math.Atan2(2*xy, x2-y2)}, true
}

// Grid indexes points for neighbour searches in pixel space.
type Grid struct {
	cell float64
	pts  [][2]float64
	m    map[[2]int][]int
}

func NewGrid(pts [][2]float64, cell float64) *Grid {
	g := &Grid{cell: cell, pts: pts, m: make(map[[2]int][]int, len(pts))}
	for i, p := range pts {
		k := [2]int{int(math.Floor(p[0] / cell)), int(math.Floor(p[1] / cell))}
		g.m[k] = append(g.m[k], i)
	}
	return g
}

// Within returns the points within r of (x, y).
func (g *Grid) Within(x, y, r float64) []int {
	var out []int
	n := int(math.Ceil(r / g.cell))
	cx, cy := int(math.Floor(x/g.cell)), int(math.Floor(y/g.cell))
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			for _, i := range g.m[[2]int{cx + dx, cy + dy}] {
				p := g.pts[i]
				if (p[0]-x)*(p[0]-x)+(p[1]-y)*(p[1]-y) <= r*r {
					out = append(out, i)
				}
			}
		}
	}
	return out
}

// Nearest returns the nearest point within r of (x, y) and its distance,
// or -1.
func (g *Grid) Nearest(x, y, r float64) (int, float64) {
	best, bd := -1, math.Inf(1)
	for _, i := range g.Within(x, y, r) {
		p := g.pts[i]
		if d := math.Hypot(p[0]-x, p[1]-y); d < bd {
			best, bd = i, d
		}
	}
	return best, bd
}
