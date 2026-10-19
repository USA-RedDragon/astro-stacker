package discoveries

import (
	"math"
	"slices"
)

const (
	// noiseBox is the side of the cells a difference's local noise is
	// measured in. The background mesh's noise is median filtered over 3x3
	// of its 64-px boxes, which read M 31's nucleus 2.0 to 3.0 times too
	// quiet on 2025-10-17, 10-26 and 10-27: the noise there was the cell's
	// own, not its neighbours'.
	noiseBox = 64
	// noiseDraws is how many empty apertures are tried in a cell, and
	// noiseMinKept the fewest that must be usable for the cell's own
	// noise: the robust spread of 100 is good to about 12%.
	noiseDraws   = 400
	noiseMinKept = 100
)

// MinLocalSNR is the least strength against the noise around it that a
// brightening where no star was may have to be promoted. On M 31's bulge
// the difference is 1.5 to 3.4 times noisier than the picture as a whole:
// in the audit of October 2026, 58 of the 82 nova candidates stood under 5
// times the noise around them though at 7 to 12.8 times the picture's
// (60465 on 2025-10-26: 12.8 against 1.7), while all 27 of the app's
// brightenings at real M 31 novae stood at 5.68 or more (AT 2025abrs, H-a,
// 2025-11-11) and AT 2026acsi at 6.8 to 7.4.
const MinLocalSNR = 4.5

// noiseMap is the scatter of empty apertures of radius apertureR in each
// noiseBox cell of a difference: 1.4826 times the median absolute deviation
// of the sums at up to noiseDraws spots spread over the cell, away from the
// reference's sources and the edge. A cell the reference's sources mostly
// cover, as a galaxy's bright core, is measured at all its spots off the
// edge instead: there the picture's noise is the one most wrong. A cell
// with too few spots, at the edge, has none.
type noiseMap struct {
	nx, ny int
	sigma  []float64
	global float64
}

// newNoiseMap measures the local noise of diff, whose reference is ref and
// whose picture-wide empty-aperture noise is global.
func newNoiseMap(diff, ref *Plane, edge []bool, global float64) noiseMap {
	w, h := diff.W, diff.H
	m := noiseMap{nx: (w + noiseBox - 1) / noiseBox, ny: (h + noiseBox - 1) / noiseBox, global: global}
	m.sigma = make([]float64, m.nx*m.ny)
	mask := sourceMask(ref, edge)
	half := int(math.Ceil(apertureR + 0.5))
	kern := apertureKernel(apertureR, half)
	side := 2*half + 1
	empty := make([]float64, 0, noiseDraws)
	all := make([]float64, 0, noiseDraws)
	for cy := range m.ny {
		for cx := range m.nx {
			x0, y0 := cx*noiseBox, cy*noiseBox
			cw, ch := min(noiseBox, w-x0), min(noiseBox, h-y0)
			empty, all = empty[:0], all[:0]
			for i := 1; i <= noiseDraws; i++ {
				x := x0 + int(halton(i, 2)*float64(cw))
				y := y0 + int(halton(i, 3)*float64(ch))
				if x < half || y < half || x >= w-half || y >= h-half || edge[y*w+x] {
					continue
				}
				var s float64
				ok := true
				for k, wt := range kern {
					v := diff.Data[(y+k/side-half)*w+x+k%side-half]
					if isNaN32(v) {
						ok = false
						break
					}
					s += wt * float64(v)
				}
				if !ok {
					continue
				}
				all = append(all, s)
				if !mask[y*w+x] {
					empty = append(empty, s)
				}
			}
			v := math.NaN()
			switch {
			case len(empty) >= noiseMinKept:
				v = robustStd(empty)
			case len(all) >= noiseMinKept:
				v = robustStd(all)
			}
			m.sigma[cy*m.nx+cx] = v
		}
	}
	return m
}

// at is the noise of an aperture at (x, y): the cells' noise interpolated
// between their centres, so a spot near a cell's side is judged by both
// cells as an aperture scatter measured around it would be, and never
// under the picture's.
func (m noiseMap) at(x, y float64) float64 {
	if m.nx == 0 {
		return m.global
	}
	fx := x/noiseBox - 0.5
	fy := y/noiseBox - 0.5
	ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := fx-float64(ix), fy-float64(iy)
	var sum, wsum float64
	for _, c := range [4][3]float64{{0, 0, (1 - tx) * (1 - ty)}, {1, 0, tx * (1 - ty)}, {0, 1, (1 - tx) * ty}, {1, 1, tx * ty}} {
		cx := min(max(ix+int(c[0]), 0), m.nx-1)
		cy := min(max(iy+int(c[1]), 0), m.ny-1)
		if v := m.sigma[cy*m.nx+cx]; !math.IsNaN(v) && c[2] > 0 {
			sum += c[2] * v
			wsum += c[2]
		}
	}
	if !(wsum > 0) {
		return m.global
	}
	return math.Max(m.global, sum/wsum)
}

// apertureKernel is the weight Aperture gives each pixel of the
// (2*half+1)^2 square about an aperture of radius r centred on a pixel.
func apertureKernel(r float64, half int) []float64 {
	side := 2*half + 1
	p := NewPlane(side, side)
	k := make([]float64, side*side)
	for i := range k {
		p.Data[i] = 1
		k[i] = Aperture(p, float64(half), float64(half), r)
		p.Data[i] = 0
	}
	return k
}

// robustStd is 1.4826 times the median absolute deviation of v.
func robustStd(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return 1.4826 * mad(s, median64(s))
}
