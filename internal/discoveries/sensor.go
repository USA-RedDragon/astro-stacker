package discoveries

import "math"

// sensorMap takes a registered sub's pixel back to the camera's sensor, to
// first order in the sub's small rotation against the reference frame:
// sensor x = x + θy − u and sensor y = y − θx − v. The registration's
// transform is not kept, so it is read off the sub's footprint, the edges
// of the area its pixels cover. A hot pixel stays on one sensor pixel
// while the registration walks it across the sky with the night's drift
// and dithers, which links it into a track: on Cygnis Loop Panel 2's
// broadband nights of 2026-10-05 to 07 every one of the 76 to 125 unknown
// movers a night was such a pixel.
type sensorMap struct {
	theta, u, v float64
	ok          bool
}

// at is the sensor pixel under the registered pixel (x, y).
func (m sensorMap) at(x, y float64) (sx, sy float64) {
	return x + m.theta*y - m.u, y - m.theta*x - m.v
}

const (
	footprintStep   = 8
	footprintClip   = 2.0
	footprintPasses = 8
	minFootprint    = 50
)

// footprintEq is one point of a sub's footprint edge: a·(θ, u, v) = rhs.
type footprintEq struct {
	a   [3]float64
	rhs float64
}

// firstLast are the first and last of n indices where covered is true,
// -1 when there is none.
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

// footprintEdges are the points where every 8th row and column of a sub
// leave the area its pixels cover, inside the frame.
func footprintEdges(p *Plane) (eqs []footprintEq, rows, cols int) {
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

// footprint fits a sub's sensorMap to the first and last covered pixel of
// every 8th row and column that lie inside the frame, leaving out by
// their residuals the points near a corner, which lie on the other edge. An axis with none,
// because the sub covers it from edge to edge, is not shifted along it;
// a sub that covers the whole frame is the reference itself.
func footprint(p *Plane) sensorMap {
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
		clip := math.Max(footprintClip, 3*1.4826*median64(kept))
		for i := range eqs {
			use[i] = res[i] <= clip
		}
	}
	return sensorMap{theta: sol[0], u: sol[1], v: sol[2], ok: true}
}
