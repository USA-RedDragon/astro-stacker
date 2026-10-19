package period

import (
	"math"
	"slices"
)

// TESSComp is one other star's light curve in a TESS sector, from the
// same TESS job: T in BJD, F its flux and E the flux errors. Blended says
// it lies within a few TESS pixels of the star, so the star's own dips
// leak into it.
type TESSComp struct {
	Sector  int
	Blended bool
	T, F, E []float64
}

// Shared TESS windows. In sector 41 of 16040 a 0.26 deep, 3.5 h flat
// drop came 2 h before one predicted minimum in the star and all five
// stars within 8", and scattered light near the mid-sector gap dimmed
// stars across the field at another; elsewhere in the sector the five
// stars only carry the star's own dips, the same amount at each minimum.
const (
	// tessNearZ is how far, in the spread of its minima, a star within a
	// few pixels must move in a window, the same way as most of the
	// others there, to count as moving with the star.
	tessNearZ = 2.0
	// tessNearShare is the share of those stars that must.
	tessNearShare = 0.5
	// tessFarZ is how far, either way, a star further out must move.
	tessFarZ = 3.0
	// tessFarShare is the share of those stars that must.
	tessFarShare = 1.0 / 3
	// tessCompMin is how many stars must move together.
	tessCompMin = 2
	// tessCompBase is how many other minima a blended star needs for its
	// usual share of the star's dips.
	tessCompBase = 2
)

type compWin struct {
	k        int
	tm, tau  float64
	amp, err float64
	ok       bool
}

// tessShared marks the windows of one sector's TESS minima, in order,
// where the other stars of the sector move by more than usual at the
// time of the star's fitted minimum: the star's dip there is not its own.
func (a *analysis) tessShared(sector, group int, fits []tessFit, taus []float64) []bool {
	out := make([]bool, len(fits))
	if a.opts.TESSComparisons == nil {
		return out
	}
	if a.tessComps == nil {
		a.tessComps = a.opts.TESSComparisons()
		if a.tessComps == nil {
			a.tessComps = []TESSComp{}
		}
	}
	m := a.ecl
	var near, far [3][]int
	for i := range near {
		near[i], far[i] = make([]int, len(fits)), make([]int, len(fits))
	}
	for _, c := range a.tessComps {
		if c.Sector != sector || len(c.T) < 10 {
			continue
		}
		cnt := &far
		if c.Blended {
			cnt = &near
		}
		noise := compNoise(c.F)
		if !(noise > 0) {
			continue
		}
		ws := make([]compWin, len(fits))
		for i, f := range fits {
			half := m.dips[f.k].width * m.P
			ws[i] = compWin{k: f.k, tm: f.tm, tau: taus[i]}
			ws[i].amp, ws[i].err, ws[i].ok = a.compAmp(c, f.k, f.tm+taus[i], 1.5*half, noise)
		}
		var ratios []float64
		for _, w := range ws {
			if d := m.depth[group][w.k]; w.ok && d > 0 {
				ratios = append(ratios, w.amp/d)
			}
		}
		leak := 0.0
		switch {
		case len(ratios) >= tessCompBase+1:
			leak = median(ratios)
		case c.Blended:
			continue
		}
		res := make([]float64, len(ws))
		var spreadOf []float64
		for i, w := range ws {
			if w.ok {
				res[i] = w.amp - leak*m.depth[group][w.k]
				spreadOf = append(spreadOf, res[i])
			}
		}
		spread := 0.0
		if len(spreadOf) >= 5 {
			spread = 1.4826 * medianAbs(spreadOf)
		}
		for i, w := range ws {
			if !w.ok {
				continue
			}
			cnt[0][i]++
			z := res[i] / math.Max(w.err, spread)
			lim := tessFarZ
			if c.Blended {
				lim = tessNearZ
			}
			if z > lim {
				cnt[1][i]++
			} else if z < -lim {
				cnt[2][i]++
			}
		}
	}
	for i := range fits {
		nb := max(near[1][i], near[2][i])
		nf := far[1][i] + far[2][i]
		out[i] = (nb >= tessCompMin && float64(nb) >= tessNearShare*float64(near[0][i])) ||
			(nf >= tessCompMin && float64(nf) >= tessFarShare*float64(far[0][i]))
	}
	return out
}

// compAmp fits comparison c's flux within win of the time tm, relative to
// tref, with a constant less the shape of dip k: the amount it dims in
// the shape of the star's dip, and its error from the star's noise.
func (a *analysis) compAmp(c TESSComp, k int, tm, win, noise float64) (float64, float64, bool) {
	m := a.ecl
	dp := m.dips[k]
	var n, sx, sy, sxx, sxy float64
	lo, _ := slices.BinarySearch(c.T, tm+a.f.tref-win)
	for i := lo; i < len(c.T); i++ {
		dt := c.T[i] - a.f.tref - tm
		if dt > win {
			break
		}
		if math.IsNaN(c.F[i]) {
			continue
		}
		x := -shapeOnly(dt/m.P, dp.width, dp.shape)
		n++
		sx += x
		sy += c.F[i]
		sxx += x * x
		sxy += x * c.F[i]
	}
	det := n*sxx - sx*sx
	if n < 5 || !(det > 0) {
		return 0, 0, false
	}
	amp := (n*sxy - sx*sy) / det
	return amp, noise * math.Sqrt(n/det), true
}

// compNoise is the point to point scatter of a flux series.
func compNoise(f []float64) float64 {
	d := make([]float64, 0, len(f))
	for i := 1; i < len(f); i++ {
		if !math.IsNaN(f[i]) && !math.IsNaN(f[i-1]) {
			d = append(d, f[i]-f[i-1])
		}
	}
	if len(d) < 10 {
		return math.NaN()
	}
	return 1.4826 * medianAbs(d) / math.Sqrt2
}

func medianAbs(v []float64) float64 {
	a := make([]float64, len(v))
	for i, x := range v {
		a[i] = math.Abs(x)
	}
	return median(a)
}
