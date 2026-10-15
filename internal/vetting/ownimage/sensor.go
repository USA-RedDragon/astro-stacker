package ownimage

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

type sensorMap struct {
	theta, u, v float64
	ok          bool
}

func (m sensorMap) at(x, y float64) (sx, sy float64) {
	return x + m.theta*y - m.u, y - m.theta*x - m.v
}

const (
	footprintStep   = 8
	footprintClip   = 2.0
	footprintPasses = 8
	minFootprint    = 50
)

type footprintEq struct {
	a   [3]float64
	rhs float64
}

func firstLast(n int, covered func(int) bool) (first, last int) {
	first, last = -1, -1
	for i := range n {
		if covered(i) {
			first = i
			break
		}
	}
	if first < 0 {
		return -1, -1
	}
	for i := n - 1; i >= 0; i-- {
		if covered(i) {
			last = i
			break
		}
	}
	return first, last
}

func footprintEdges(p *discoveries.Plane) (eqs []footprintEq, rows, cols int) {
	w, h := p.W, p.H
	for y := 0; y < h; y += footprintStep {
		l, r := firstLast(w, func(x int) bool { return !isNaN32(p.Data[y*w+x]) })
		fy := float64(y)
		if l > 0 {
			eqs = append(eqs, footprintEq{[3]float64{fy, -1, 0}, -float64(l)})
			rows++
		}
		if r >= 0 && r < w-1 {
			eqs = append(eqs, footprintEq{[3]float64{fy, -1, 0}, float64(w-1) - float64(r)})
			rows++
		}
	}
	for x := 0; x < w; x += footprintStep {
		t, b := firstLast(h, func(y int) bool { return !isNaN32(p.Data[y*w+x]) })
		fx := float64(x)
		if t > 0 {
			eqs = append(eqs, footprintEq{[3]float64{-fx, 0, -1}, -float64(t)})
			cols++
		}
		if b >= 0 && b < h-1 {
			eqs = append(eqs, footprintEq{[3]float64{-fx, 0, -1}, float64(h-1) - float64(b)})
			cols++
		}
	}
	return eqs, rows, cols
}

// footprint recovers a registered sub's shift and small rotation against
// the reference frame from the edges of the area its pixels cover, as
// the discoveries package does to follow a hot pixel back to the sensor.
func footprint(p *discoveries.Plane) sensorMap {
	eqs, rows, cols := footprintEdges(p)
	if rows == 0 {
		eqs = append(eqs, footprintEq{[3]float64{0, 1e3, 0}, 0})
	}
	if cols == 0 {
		eqs = append(eqs, footprintEq{[3]float64{0, 0, 1e3}, 0})
	}
	if rows+cols == 0 {
		return sensorMap{ok: true}
	}
	if rows+cols < minFootprint {
		return sensorMap{}
	}
	use := make([]bool, len(eqs))
	for i := range use {
		use[i] = true
	}
	var sol [3]float64
	for range footprintPasses {
		var ata [3][3]float64
		var atb [3]float64
		for i, e := range eqs {
			if !use[i] {
				continue
			}
			for r := range 3 {
				atb[r] += e.a[r] * e.rhs
				for c := range 3 {
					ata[r][c] += e.a[r] * e.a[c]
				}
			}
		}
		s, ok := solve3(ata, atb)
		if !ok {
			return sensorMap{}
		}
		sol = s
		res := make([]float64, len(eqs))
		var kept []float64
		for i, e := range eqs {
			res[i] = math.Abs(e.a[0]*sol[0] + e.a[1]*sol[1] + e.a[2]*sol[2] - e.rhs)
			if use[i] {
				kept = append(kept, res[i])
			}
		}
		clip := math.Max(footprintClip, 3*1.4826*median(kept))
		for i := range eqs {
			use[i] = res[i] <= clip
		}
	}
	return sensorMap{theta: sol[0], u: sol[1], v: sol[2], ok: true}
}

func det3(a [3][3]float64) float64 {
	return a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
}

func solve3(a [3][3]float64, b [3]float64) ([3]float64, bool) {
	det := det3(a)
	if math.Abs(det) < 1e-300 {
		return [3]float64{}, false
	}
	var x [3]float64
	for c := range 3 {
		m := a
		for r := range 3 {
			m[r][c] = b[r]
		}
		x[c] = det3(m) / det
	}
	return x, true
}
