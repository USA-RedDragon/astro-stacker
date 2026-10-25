package stacking

import (
	"math"
	"slices"
)

// objectMask marks the blocks that are the target: at least
// flatObjectSigma above their quadratic sky in any narrowband plane, or in
// two thirds of the broadband ones, grown by two blocks. grid returns a
// plane's blocks, quadratic sky and block noise.
func objectMask(names []string, grid func(string) ([]float64, []float64, float64), gw, gh int) []bool {
	n := gw * gh
	z := map[string][]float64{}
	var broad []string
	for _, f := range names {
		z[f] = skyZ(grid(f))
		if !narrowband(f) {
			broad = append(broad, f)
		}
	}
	score := make([]float64, n)
	vals := make([]float64, 0, len(broad))
	for i := range score {
		for _, f := range names {
			if narrowband(f) && !math.IsNaN(z[f][i]) {
				score[i] = max(score[i], z[f][i])
			}
		}
		vals = vals[:0]
		for _, f := range broad {
			if v := z[f][i]; !math.IsNaN(v) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			continue
		}
		slices.Sort(vals)
		score[i] = max(score[i], vals[(len(vals)-1)/3], -vals[len(vals)-1-(len(vals)-1)/3])
	}
	mask := growMask(score, gw, gh)
	for range 2 {
		grown := slices.Clone(mask)
		for i, m := range mask {
			if m {
				forNeighbours(i, gw, gh, func(j int) { grown[j] = true })
			}
		}
		mask = grown
	}
	return mask
}

// skyZ is how far each block sits above the sky q, in noise, less the
// median of that over the blocks; NaN where blocks has none.
func skyZ(blocks, q []float64, noise float64) []float64 {
	z := make([]float64, len(blocks))
	var vals []float64
	for i := range z {
		z[i] = math.NaN()
		if !math.IsNaN(blocks[i]) && noise > 0 {
			z[i] = (blocks[i] - q[i]) / noise
			vals = append(vals, z[i])
		}
	}
	if len(vals) > 0 {
		slices.Sort(vals)
		off := vals[len(vals)/2]
		for i := range z {
			z[i] -= off
		}
	}
	return z
}

// growMask marks the blocks scoring past flatObjectSigma, and from them
// the blocks next to marked ones scoring past flatGrowSigma, short of the
// frame's edge.
func growMask(score []float64, gw, gh int) []bool {
	mask := make([]bool, len(score))
	var queue []int
	for i, v := range score {
		if v > flatObjectSigma {
			mask[i] = true
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		forNeighbours(i, gw, gh, func(j int) {
			xx, yy := j%gw, j/gw
			if xx < flatEdge || xx >= gw-flatEdge || yy < flatEdge || yy >= gh-flatEdge {
				return
			}
			if !mask[j] && score[j] > flatGrowSigma {
				mask[j] = true
				queue = append(queue, j)
			}
		})
	}
	return mask
}

// forNeighbours calls f with block i and each of its neighbours in the
// gw×gh grid.
func forNeighbours(i, gw, gh int, f func(int)) {
	x, y := i%gw, i/gw
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if xx, yy := x+dx, y+dy; xx >= 0 && xx < gw && yy >= 0 && yy < gh {
				f(yy*gw + xx)
			}
		}
	}
}

// gaussFill is the normalised Gaussian convolution of the blocks in use,
// blended toward the surface at twice the scale where their weight is
// under a third of a full neighbourhood's, and toward quad past 32 blocks.
func gaussFill(blocks []float64, use []bool, quad []float64, gw, gh int, scale float64) []float64 {
	var coarse []float64
	if scale >= 32 {
		coarse = quad
	} else {
		coarse = gaussFill(blocks, use, quad, gw, gh, 2*scale)
	}
	r := int(math.Ceil(3 * scale))
	k := make([]float64, 2*r+1)
	full := 0.0
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * scale * scale))
		full += k[i]
	}
	full *= full
	num := make([]float64, len(blocks))
	den := make([]float64, len(blocks))
	tn := make([]float64, len(blocks))
	td := make([]float64, len(blocks))
	for y := range gh {
		for x := range gw {
			var a, b float64
			for i, kv := range k {
				xx := x + i - r
				if xx < 0 || xx >= gw || !use[y*gw+xx] {
					continue
				}
				a += kv * blocks[y*gw+xx]
				b += kv
			}
			tn[y*gw+x], td[y*gw+x] = a, b
		}
	}
	for y := range gh {
		for x := range gw {
			var a, b float64
			for i, kv := range k {
				yy := y + i - r
				if yy < 0 || yy >= gh {
					continue
				}
				a += kv * tn[yy*gw+x]
				b += kv * td[yy*gw+x]
			}
			num[y*gw+x], den[y*gw+x] = a, b
		}
	}
	out := make([]float64, len(blocks))
	for i := range out {
		wt := math.Min(1, den[i]/(full/3))
		v := coarse[i]
		if den[i] > 0 {
			v = wt*num[i]/den[i] + (1-wt)*coarse[i]
		}
		out[i] = v
	}
	return out
}

// structure is the sky's large-scale structure left by a background
// surface, in pixel noise pix: the RMS, over the sky blocks, of the
// residual blocks smoothed over two blocks.
func structure(blocks, surface []float64, mask []bool, gw, gh int, pix float64) float64 {
	if pix <= 0 {
		return 0
	}
	use := make([]bool, len(blocks))
	res := make([]float64, len(blocks))
	var all []float64
	for i, v := range blocks {
		if !math.IsNaN(v) && !mask[i] {
			use[i] = true
			res[i] = v - surface[i]
			all = append(all, res[i])
		}
	}
	if len(all) < 12 {
		return 0
	}
	zero := make([]float64, len(blocks))
	sm := gaussFill(res, use, zero, gw, gh, 1)
	slices.Sort(all)
	off := all[len(all)/2]
	var sum float64
	n := 0
	for i, u := range use {
		if u {
			d := sm[i] - off
			sum += d * d
			n++
		}
	}
	return math.Sqrt(sum/float64(n)) / pix
}
