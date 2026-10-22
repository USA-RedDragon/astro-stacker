package ownimage

import (
	"math"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	fitR        = 11.0
	fitReach    = fitR + 5
	fitClip     = 5.0
	fitPasses   = 4
	minFitPix   = 50
	minSigma    = 0.5
	maxSigma    = 5.0
	maxShift    = 3.0
	shapeIters  = 120
	startSigma  = 1.3
	shapeBright = 5
	fitTiny     = 1e-9
	// blendReach is how close, in pixels, a Gaia neighbour must be to a
	// star for the fit to count its light as the star's, in every sub alike:
	// source 15523 has G 18.7 and G 19.5 neighbours 0.9 and 1.4 px away at
	// 1.9"/px, and fitting the three apart threw its flux about by 0.3 mag
	// a sub. A reach that followed each sub's star width took source 236's
	// G 17.2 neighbour 2.0 px away into the star in the blurrier subs only,
	// moving it 0.07 mag between subs and its eclipse 16 min late.
	blendReach = 2.5
)

// starModel is one star to fit and the Gaia stars around it, all at their
// reference frame pixel positions.
type starModel struct {
	x, y float64
	nbs  [][2]float64
	// gs are the neighbours' Gaia G, NaN where not known.
	gs   []float64
	self int
	// g is the star's Gaia G, 0 for a spot with no star.
	g float64
}

// merges says whether neighbour j is close enough to a catalogued star
// for its light to count as the star's.
func (m *starModel) merges(j int) bool {
	if !(m.g > 0) || j == m.self {
		return false
	}
	self := m.nbs[m.self]
	return math.Hypot(m.nbs[j][0]-self[0], m.nbs[j][1]-self[1]) < blendReach
}

// blendShare is the light of the neighbours the fit counts as the star's,
// as a share of the star's own light, by their Gaia G.
func (m *starModel) blendShare() float64 {
	var s float64
	for j := range m.nbs {
		if m.merges(j) && j < len(m.gs) && finite(m.gs[j]) {
			s += math.Pow(10, -0.4*(m.gs[j]-m.g))
		}
	}
	return s
}

type fitOut struct {
	flux, err, rss float64
	ok             bool
}

type pix struct {
	x, y int
	v    float64
}

func regionOf(p *discoveries.Plane, cx, cy float64) []pix {
	x0, x1 := int(math.Floor(cx-fitR)), int(math.Ceil(cx+fitR))
	y0, y1 := int(math.Floor(cy-fitR)), int(math.Ceil(cy+fitR))
	out := make([]pix, 0, 400)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy >= fitR*fitR {
				continue
			}
			v := p.At(x, y)
			if isNaN32(v) {
				continue
			}
			out = append(out, pix{x, y, float64(v)})
		}
	}
	return out
}

// fitStar fits Gaussians of one shared width to the star and its
// neighbours with a sloped plane for the sky, leaving out pixels more than
// 5 times the spread from the fit when clip is set, and returns the star's
// flux and its error. When clip is set, the flux of a catalogued star's
// neighbours closer than blendReach is counted as the star's: the fit
// parts close stars poorly but their sum well, and counting them in every
// sub alike keeps the star's light the same whatever the sub's width.
func fitStar(p *discoveries.Plane, m *starModel, sigma, sx, sy float64, clip bool) fitOut {
	cx, cy := m.x+sx, m.y+sy
	px := regionOf(p, cx, cy)
	if len(px) < minFitPix {
		return fitOut{}
	}
	k := len(m.nbs)
	n := k + 3
	x0, y0 := int(math.Floor(cx-fitR)), int(math.Floor(cy-fitR))
	side := int(2*fitR) + 3
	gx := make([]float64, k*side)
	gy := make([]float64, k*side)
	s2 := 2 * sigma * sigma
	norm := 1 / (math.Pi * s2)
	for j, nb := range m.nbs {
		for i := range side {
			dx := float64(x0+i) - (nb[0] + sx)
			dy := float64(y0+i) - (nb[1] + sy)
			gx[j*side+i] = math.Exp(-dx * dx / s2)
			gy[j*side+i] = math.Exp(-dy * dy / s2)
		}
	}
	row := make([]float64, n)
	act := make([]int, 0, n)
	fill := func(q pix) {
		ix, iy := q.x-x0, q.y-y0
		act = act[:0]
		for j := range k {
			v := gx[j*side+ix] * gy[j*side+iy]
			if v > fitTiny {
				row[j] = norm * v
				act = append(act, j)
			}
		}
		row[k] = 1
		row[k+1] = (float64(q.x) - cx) / fitR
		row[k+2] = (float64(q.y) - cy) / fitR
		act = append(act, k, k+1, k+2)
	}
	use := make([]bool, len(px))
	for i := range use {
		use[i] = true
	}
	res := make([]float64, len(px))
	var coef, ata []float64
	var s float64
	for pass := range fitPasses {
		ata = make([]float64, n*n)
		atb := make([]float64, n)
		used := 0
		for i, q := range px {
			if !use[i] {
				continue
			}
			used++
			fill(q)
			for ai, a := range act {
				atb[a] += row[a] * q.v
				for _, b := range act[ai:] {
					ata[a*n+b] += row[a] * row[b]
				}
			}
		}
		if used < minFitPix {
			return fitOut{}
		}
		for a := range n {
			for b := range a {
				ata[a*n+b] = ata[b*n+a]
			}
		}
		c, ok := solveSPD(ata, atb, n)
		if !ok {
			return fitOut{}
		}
		coef = c
		var kept []float64
		for i, q := range px {
			fill(q)
			var mv float64
			for _, a := range act {
				mv += row[a] * coef[a]
			}
			res[i] = q.v - mv
			if use[i] {
				kept = append(kept, res[i])
			}
		}
		if !clip {
			break
		}
		s = robustStd(kept)
		if pass == fitPasses-1 || !(s > 0) {
			break
		}
		if !reclip(res, use, s) {
			break
		}
	}
	rss := keptRSS(res, use)
	if !clip {
		return fitOut{flux: coef[m.self], rss: rss, ok: true}
	}
	e := make([]float64, n)
	var flux float64
	for j := range k {
		if j == m.self || m.merges(j) {
			e[j] = 1
			flux += coef[j]
		}
	}
	z, ok := solveSPD(ata, e, n)
	if !ok {
		return fitOut{}
	}
	var v float64
	for j := range k {
		v += e[j] * z[j]
	}
	if !(v > 0) {
		return fitOut{}
	}
	return fitOut{flux: flux, err: s * math.Sqrt(v), rss: rss, ok: true}
}

// reclip keeps the pixels whose residual is under fitClip times s and
// reports whether the kept set changed.
func reclip(res []float64, use []bool, s float64) bool {
	changed := false
	for i := range res {
		u := math.Abs(res[i]) < fitClip*s
		if u != use[i] {
			changed = true
		}
		use[i] = u
	}
	return changed
}

// keptRSS is the sum of the squared residuals of the kept pixels.
func keptRSS(res []float64, use []bool) float64 {
	var rss float64
	for i := range res {
		if use[i] {
			rss += res[i] * res[i]
		}
	}
	return rss
}

// solveSPD solves a symmetric positive definite system by Cholesky, with a
// tiny ridge so columns of stars far outside the fit do not break it.
func solveSPD(a, b []float64, n int) ([]float64, bool) {
	l := make([]float64, n*n)
	var tr float64
	for i := range n {
		tr += a[i*n+i]
	}
	ridge := 1e-10 * tr / float64(n)
	for i := range n {
		for j := 0; j <= i; j++ {
			s := a[i*n+j]
			if i == j {
				s += ridge
			}
			for k := range j {
				s -= l[i*n+k] * l[j*n+k]
			}
			if i == j {
				if !(s > 0) {
					return nil, false
				}
				l[i*n+i] = math.Sqrt(s)
			} else {
				l[i*n+j] = s / l[j*n+j]
			}
		}
	}
	y := make([]float64, n)
	for i := range n {
		s := b[i]
		for k := range i {
			s -= l[i*n+k] * y[k]
		}
		y[i] = s / l[i*n+i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for k := i + 1; k < n; k++ {
			s -= l[k*n+i] * x[k]
		}
		x[i] = s / l[i*n+i]
	}
	return x, true
}

// nelderMead minimizes f from x0 with the given first steps.
func nelderMead(f func([]float64) float64, x0, step []float64, iters int) ([]float64, float64) {
	d := len(x0)
	pts := make([][]float64, d+1)
	vals := make([]float64, d+1)
	for i := range pts {
		pts[i] = append([]float64(nil), x0...)
		for j := range d {
			if j == i-1 {
				pts[i][j] += step[j]
			}
		}
		vals[i] = f(pts[i])
	}
	idx := make([]int, d+1)
	for range iters {
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return vals[idx[a]] < vals[idx[b]] })
		best, worst, second := idx[0], idx[d], idx[d-1]
		if math.Abs(vals[worst]-vals[best]) <= 1e-12*(math.Abs(vals[best])+1e-30) {
			break
		}
		cen := make([]float64, d)
		for _, i := range idx[:d] {
			for j := range d {
				cen[j] += pts[i][j] / float64(d)
			}
		}
		at := func(t float64) []float64 {
			out := make([]float64, d)
			for j := range d {
				out[j] = cen[j] + t*(pts[worst][j]-cen[j])
			}
			return out
		}
		r := at(-1)
		fr := f(r)
		switch {
		case fr < vals[best]:
			e := at(-2)
			if fe := f(e); fe < fr {
				pts[worst], vals[worst] = e, fe
			} else {
				pts[worst], vals[worst] = r, fr
			}
		case fr < vals[second]:
			pts[worst], vals[worst] = r, fr
		default:
			c := at(0.5)
			if fc := f(c); fc < vals[worst] {
				pts[worst], vals[worst] = c, fc
				continue
			}
			for _, i := range idx[1:] {
				for j := range d {
					pts[i][j] = pts[best][j] + 0.5*(pts[i][j]-pts[best][j])
				}
				vals[i] = f(pts[i])
			}
		}
	}
	bi := 0
	for i := range vals {
		if vals[i] < vals[bi] {
			bi = i
		}
	}
	return pts[bi], vals[bi]
}

// fitShape finds the star width and the small shift of the sub against
// the catalogue positions that fit the bright stars best.
func fitShape(p *discoveries.Plane, bright []*starModel) (sigma, sx, sy float64, ok bool) {
	var use []*starModel
	for _, m := range bright {
		if fitStar(p, m, startSigma, 0, 0, false).ok {
			use = append(use, m)
		}
	}
	if len(use) == 0 {
		return 0, 0, 0, false
	}
	f := func(q []float64) float64 {
		if q[0] < minSigma || q[0] > maxSigma || math.Abs(q[1]) > maxShift || math.Abs(q[2]) > maxShift {
			return math.Inf(1)
		}
		var s float64
		for _, m := range use {
			o := fitStar(p, m, q[0], q[1], q[2], false)
			if !o.ok {
				return math.Inf(1)
			}
			s += o.rss
		}
		return s
	}
	best, v := nelderMead(f, []float64{startSigma, 0, 0}, []float64{0.4, 0.7, 0.7}, shapeIters)
	if math.IsInf(v, 0) {
		return 0, 0, 0, false
	}
	return best[0], best[1], best[2], true
}

// gauss2D is an elliptical Gaussian fitted to a stamp; sa is its width
// along the direction th radians from +x toward +y, sb across it.
type gauss2D struct {
	x, y, sa, sb, th, amp float64
	ok                    bool
}

func (g gauss2D) width() float64 { return math.Sqrt(g.sa * g.sb) }

func (g gauss2D) elongation() float64 { return math.Max(g.sa, g.sb) / math.Min(g.sa, g.sb) }

// fitGauss2D fits an elliptical Gaussian with a flat sky to the pixels of
// st (side n) within r of (cx, cy).
func fitGauss2D(st []float64, n int, cx, cy, r float64) gauss2D {
	type px struct{ x, y, v float64 }
	var pts []px
	for y := range n {
		for x := range n {
			dx, dy := float64(x)-cx, float64(y)-cy
			v := st[y*n+x]
			if dx*dx+dy*dy > r*r || !finite(v) {
				continue
			}
			pts = append(pts, px{float64(x), float64(y), v})
		}
	}
	if len(pts) < 12 {
		return gauss2D{}
	}
	solve := func(q []float64) (amp, rss float64) {
		ca, sa := math.Cos(q[4]), math.Sin(q[4])
		var s11, s12, s22, b1, b2 float64
		g := make([]float64, len(pts))
		for i, p := range pts {
			dx, dy := p.x-q[0], p.y-q[1]
			u, w := ca*dx+sa*dy, -sa*dx+ca*dy
			g[i] = math.Exp(-0.5 * (u*u/(q[2]*q[2]) + w*w/(q[3]*q[3])))
			s11 += g[i] * g[i]
			s12 += g[i]
			s22++
			b1 += g[i] * p.v
			b2 += p.v
		}
		det := s11*s22 - s12*s12
		if !(math.Abs(det) > 0) {
			return 0, math.Inf(1)
		}
		amp = (b1*s22 - b2*s12) / det
		bg := (s11*b2 - s12*b1) / det
		for i, p := range pts {
			d := p.v - amp*g[i] - bg
			rss += d * d
		}
		return amp, rss
	}
	f := func(q []float64) float64 {
		if q[2] < 0.3 || q[3] < 0.3 || q[2] > 8 || q[3] > 8 || math.Hypot(q[0]-cx, q[1]-cy) > 3 {
			return math.Inf(1)
		}
		_, rss := solve(q)
		return rss
	}
	best := []float64{cx, cy, 1.5, 1.5, 0}
	bv := math.Inf(1)
	for _, th := range []float64{0, math.Pi / 4} {
		q, v := nelderMead(f, []float64{cx, cy, 1.5, 1.2, th}, []float64{0.5, 0.5, 0.5, 0.4, 0.5}, 400)
		if v < bv {
			best, bv = q, v
		}
	}
	if math.IsInf(bv, 0) {
		return gauss2D{}
	}
	amp, _ := solve(best)
	return gauss2D{x: best[0], y: best[1], sa: best[2], sb: best[3], th: best[4], amp: amp, ok: true}
}
