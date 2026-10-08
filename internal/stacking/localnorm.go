package stacking

import (
	"math"
	"slices"
)

// Local normalization matches each sub to a reference master before it is
// stacked: the sub's sky, gradients included, becomes a smooth surface P and
// its transparency a scale s, from sub/exposure = P(x,y) + s·reference. The
// target's own light is in both, so it cancels and only the sky is fitted.
// Subs normalized this way agree pixel for pixel, so gradients from the moon
// or light pollution don't mix into the master or disturb rejection.

// unevenCoverage is the share of the frame, where the master has data, that
// fewer than 90% of its subs cover, above which subs are normalized. With
// even coverage every pixel averages the same subs' gradients and local
// normalization only costs signal to noise: on 39 Cosmic Bat subs over 10
// nights, 97-100% covered, it lowered SNR by about 1% and changed nothing
// visible. Where coverage steps (a flip, a rotated night) the subs on each
// side bring different gradients, which it removes.
const unevenCoverage = 0.1

// needsLocalNorm reports whether a master's coverage is uneven enough for
// local normalization to help.
func needsLocalNorm(acc *Accumulator) bool {
	if acc == nil || acc.Subs < 3 {
		return false
	}
	var most float32
	for _, c := range acc.Count {
		most = max(most, c)
	}
	framed, partial := 0, 0
	for i := 0; i < len(acc.Count); i += 7 {
		c := acc.Count[i]
		if c < most/2 {
			continue // outside the frame proper: dither and rotation edges
		}
		framed++
		if c < 0.9*most {
			partial++
		}
	}
	return framed > 0 && float64(partial) > unevenCoverage*float64(framed)
}

// lnCell is the side of the cells the sky is measured in, in pixels.
const lnCell = 128

// lnDegree is the degree of the sky surface: smooth enough to leave the
// target's structure alone, curved enough for moonlight and light domes.
const lnDegree = 3

// lnMinScale and lnMaxScale bound a plausible transparency scale; a fit
// outside them is treated as degenerate.
const (
	lnMinScale = 0.25
	lnMaxScale = 4
)

// skyModel is a sub's sky in counts per second, and its scale against the
// reference. The zero value is invalid; use flatSky for a plain background.
type skyModel struct {
	w, h  int
	coef  []float64 // polynomial in x and y normalized to [-1, 1]
	scale float64
	mean  float64 // P averaged over the frame, added back to the master
}

// flatSky is the model without local normalization: one background level.
func flatSky(w, h int, perSecond float64) skyModel {
	c := make([]float64, lnTerms)
	c[0] = perSecond
	return skyModel{w: w, h: h, coef: c, scale: 1, mean: perSecond}
}

const lnTerms = (lnDegree + 1) * (lnDegree + 2) / 2

// terms fills t with the polynomial's terms x^i·y^j, i+j ≤ lnDegree.
func terms(t []float64, x, y float64) {
	k := 0
	for d := 0; d <= lnDegree; d++ {
		for j := 0; j <= d; j++ {
			t[k] = math.Pow(x, float64(d-j)) * math.Pow(y, float64(j))
			k++
		}
	}
}

func (m *skyModel) nx(x int) float64 { return 2*(float64(x)+0.5)/float64(m.w) - 1 }
func (m *skyModel) ny(y int) float64 { return 2*(float64(y)+0.5)/float64(m.h) - 1 }

// row returns P along row y as a polynomial in x, lowest power first, so a
// row is evaluated with a few multiplications per pixel.
func (m *skyModel) row(y int) [lnDegree + 1]float64 {
	var r [lnDegree + 1]float64
	yy := m.ny(y)
	k := 0
	for d := 0; d <= lnDegree; d++ {
		for j := 0; j <= d; j++ {
			r[d-j] += m.coef[k] * math.Pow(yy, float64(j))
			k++
		}
	}
	return r
}

func evalRow(r [lnDegree + 1]float64, xx float64) float64 {
	v := 0.0
	for i := lnDegree; i >= 0; i-- {
		v = v*xx + r[i]
	}
	return v
}

// normalizer turns a sub's samples into normalized counts per second,
// caching the current row of the sky surface.
type normalizer struct {
	m        *skyModel
	inv      float64 // 1 / exposure
	y        int
	r        [lnDegree + 1]float64
	invScale float64
}

func (m *skyModel) normalizer(exposure float64) *normalizer {
	return &normalizer{m: m, inv: 1 / exposure, y: -1, invScale: 1 / m.scale}
}

// at returns sample v of pixel i normalized.
func (n *normalizer) at(i int, v float32) float32 {
	y := i / n.m.w
	if y != n.y {
		n.y, n.r = y, n.m.row(y)
	}
	sky := evalRow(n.r, n.m.nx(i%n.m.w))
	return float32((float64(v)*n.inv - sky) * n.invScale)
}

// fitSky fits a sub's sky against ref, the per-second sky-free mean of a
// master (ref.Mean with ref.Count samples). get returns the sub's calibrated
// sample i, 0 for no data. It falls back to flat, the sub's background level
// per second, when there is too little to fit or the fit is degenerate.
func fitSky(get func(int) float32, w, h int, exposure float64, ref *Accumulator, saturation float32, flatLevel float64) skyModel {
	flat := func() skyModel { return flatSky(w, h, flatLevel) }
	if ref == nil || ref.W != w || ref.H != h || ref.Subs < 3 {
		return flat()
	}

	cells := skyCells(get, w, h, exposure, ref, saturation)
	if len(cells) < 4*lnTerms {
		return flat()
	}

	m := skyModel{w: w, h: h}
	scale := transparency(func(i int) (float64, float64) { return cells[i].fsub, cells[i].fref }, len(cells))
	xs := make([]lnCellFit, len(cells))
	for i, c := range cells {
		xs[i] = lnCellFit{x: m.nx(int(c.x)), y: m.ny(int(c.y)), sub: c.sub, ref: scale * c.ref}
	}
	coef, ok := fitSurface(xs)
	if !ok {
		return flat()
	}
	m.coef, m.scale = coef, scale
	// The surface's mean over the frame is the sky added back to the
	// master, so it keeps a pedestal like any other stack.
	sum := 0.0
	for y := 0; y < h; y += 16 {
		r := m.row(y)
		for x := 0; x < w; x += 16 {
			sum += evalRow(r, m.nx(x))
		}
	}
	m.mean = sum / float64(((h+15)/16)*((w+15)/16))
	return m
}

// skyCell is one lnCell of a sub against the reference: their medians, the
// sub's per second, and their starlight above those medians.
type skyCell struct{ x, y, sub, ref, fsub, fref float64 }

// skyCells measures a sub against ref, cell by cell, as fitSky describes.
func skyCells(get func(int) float32, w, h int, exposure float64, ref *Accumulator, saturation float32) []skyCell {
	// Only where nearly every sub of the reference contributes: where some
	// are missing (a flip, a rotated night, dither edges) the reference
	// steps by their gradients, which a sub fitted to it would copy.
	var full float32
	for _, c := range ref.Count {
		full = max(full, c)
	}
	full *= 0.9
	var cells []skyCell
	subs := make([]float32, 0, lnCell*lnCell/4)
	refs := make([]float32, 0, lnCell*lnCell/4)
	for cy := 0; cy+lnCell/2 <= h; cy += lnCell {
		for cx := 0; cx+lnCell/2 <= w; cx += lnCell {
			subs, refs = subs[:0], refs[:0]
			x1, y1 := min(cx+lnCell, w), min(cy+lnCell, h)
			for y := cy; y < y1; y += 2 {
				for x := cx; x < x1; x += 2 {
					i := y*w + x
					v := get(i)
					if v == 0 || v >= saturation || ref.Count[i] < full {
						continue
					}
					subs = append(subs, v)
					refs = append(refs, ref.Mean[i])
				}
			}
			if len(subs) < (x1-cx)*(y1-cy)/8 { // under half the sampled pixels
				continue
			}
			// Starlight measures transparency: summed over a star it
			// doesn't depend on seeing, and above the cell's median not on
			// the sky. Stars are picked in the reference, whose noise is
			// low, and only their pixels summed: over the whole cell a
			// tiny error in the median would outweigh them.
			c := skyCell{x: float64(cx+x1) / 2, y: float64(cy+y1) / 2}
			medSub, medRef := median32(slices.Clone(subs)), median32(slices.Clone(refs))
			c.sub, c.ref = float64(medSub)/exposure, float64(medRef)
			dev := make([]float32, len(refs))
			for k, r := range refs {
				dev[k] = float32(math.Abs(float64(r - medRef)))
			}
			star := medRef + 5*madToSigma*median32(dev)
			for k, r := range refs {
				if r > star {
					c.fsub += float64(subs[k])/exposure - c.sub
					c.fref += float64(r - medRef)
				}
			}
			cells = append(cells, c)
		}
	}
	return cells
}

// transparency is the sub's flux over the reference's (see fluxRatio), or 1
// when it can't be measured or is implausible for haze.
func transparency(flux func(int) (sub, ref float64), n int) float64 {
	if s, ok := fluxRatio(flux, n); ok && s >= lnMinScale && s <= lnMaxScale {
		return s
	}
	return 1
}

// fluxRatio is the median ratio of the sub's flux over the reference's over
// cells with clearly more flux than noise, judged from the faintest half of
// the cells, which hold little but noise. ok is false when too few cells
// have flux.
func fluxRatio(flux func(int) (sub, ref float64), n int) (float64, bool) {
	type pair struct{ sub, ref float64 }
	ps := make([]pair, 0, n)
	for i := range n {
		s, r := flux(i)
		ps = append(ps, pair{s, r})
	}
	if len(ps) < 16 {
		return 0, false
	}
	slices.SortFunc(ps, func(a, b pair) int { return cmpFloat(a.ref, b.ref) })
	faint := make([]float64, len(ps)/2)
	for i := range faint {
		faint[i] = math.Abs(ps[i].sub - ps[i].ref)
	}
	slices.Sort(faint)
	floor := 5 * madToSigma * faint[len(faint)/2]
	var ratios []float64
	for _, p := range ps {
		if p.ref > floor && p.ref > 0 {
			ratios = append(ratios, p.sub/p.ref)
		}
	}
	if len(ratios) < 8 {
		return 0, false
	}
	slices.Sort(ratios)
	s := ratios[len(ratios)/2]
	return s, s > 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func median32(v []float32) float32 {
	slices.Sort(v)
	return v[len(v)/2]
}

type lnCellFit struct{ x, y, sub, ref float64 }

// fitSurface fits sub - ref = P(x,y) over the cells, dropping cells more
// than 3σ off (a nebula edge the reference sees differently, a satellite)
// and refitting.
func fitSurface(cells []lnCellFit) ([]float64, bool) {
	n := lnTerms
	keep := make([]bool, len(cells))
	for i := range keep {
		keep[i] = true
	}
	t := make([]float64, n)
	var sol []float64
	for range 5 {
		ata := make([]float64, n*n)
		atb := make([]float64, n)
		kept := 0
		for i, c := range cells {
			if !keep[i] {
				continue
			}
			kept++
			terms(t, c.x, c.y)
			for a := range n {
				atb[a] += t[a] * (c.sub - c.ref)
				for b := range n {
					ata[a*n+b] += t[a] * t[b]
				}
			}
		}
		if kept < 2*n {
			return nil, false
		}
		var ok bool
		if sol, ok = solve(ata, atb, n); !ok {
			return nil, false
		}
		resid := make([]float64, len(cells))
		var res []float64
		for i, c := range cells {
			terms(t, c.x, c.y)
			p := c.ref
			for k := range n {
				p += sol[k] * t[k]
			}
			resid[i] = math.Abs(c.sub - p)
			if keep[i] {
				res = append(res, resid[i])
			}
		}
		slices.Sort(res)
		limit := 3 * madToSigma * res[len(res)/2]
		changed := false
		for i := range cells {
			k := resid[i] <= limit
			changed = changed || k != keep[i]
			keep[i] = k
		}
		if !changed {
			break
		}
	}
	return sol, true
}

// solve solves the n×n system a·x = b by Gaussian elimination with partial
// pivoting.
func solve(a, b []float64, n int) ([]float64, bool) {
	a, b = slices.Clone(a), slices.Clone(b)
	for col := range n {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r*n+col]) > math.Abs(a[p*n+col]) {
				p = r
			}
		}
		if math.Abs(a[p*n+col]) < 1e-300 {
			return nil, false
		}
		if p != col {
			for k := range n {
				a[p*n+k], a[col*n+k] = a[col*n+k], a[p*n+k]
			}
			b[p], b[col] = b[col], b[p]
		}
		for r := col + 1; r < n; r++ {
			f := a[r*n+col] / a[col*n+col]
			for k := col; k < n; k++ {
				a[r*n+k] -= f * a[col*n+k]
			}
			b[r] -= f * b[col]
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := b[r]
		for k := r + 1; k < n; k++ {
			s -= a[r*n+k] * x[k]
		}
		x[r] = s / a[r*n+r]
	}
	for _, v := range x {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
	}
	return x, true
}
