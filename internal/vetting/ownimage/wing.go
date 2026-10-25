package ownimage

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	// wingMaxPx and wingMaxG are how near and bright a catalogued star must
	// be for its wing to be fitted at a spot with no star: the ten spots of
	// Andromeda's 2025-01-21 lay 4.0 to 7.7 px from G 13.7 to 15.8 stars
	// on nights whose star shape differed from the reference's.
	wingMaxPx = 10.0
	wingMaxG  = 16.5
	// wingFarDays is how far a night must be from every flagged night to
	// show the spot's usual level next to the star: a slow nova must not
	// be in its own baseline.
	wingFarDays = 120
	// WingSigma is the least excess over the usual level, in its errors,
	// for new light to stand clear of the star's wing: with the star
	// fitted by each sub's own star shape, 66053 showed -0.4, -0.3 and
	// -0.1 sigma on its three nights, AT 2026uti, 2.8 px from a G 15.77
	// star, +4.9 to +18.3.
	WingSigma = 3.0
	// epsfMinG and epsfMaxG bound the stars each sub's own star shape is
	// built from, epsfStars how many of the nearest isolated ones, at
	// least epsfMin of them, and epsfReach how far they may be.
	epsfMinG  = 11.5
	epsfMaxG  = 17.0
	epsfStars = 8
	epsfMin   = 3
	epsfReach = 400.0
	epsfHalf  = 10
	epsfSide  = 2*epsfHalf + 1
	// epsfCore is the radius whose light sets each star's scale.
	epsfCore = 6.0
	// wingFitR is how far beyond the star and the spot the joint fit
	// reaches, and wingMove how far the star may move from its place.
	wingFitR = 6.0
	wingMove = 1.5
	// wingFarMin is the fewest far nights that give the usual level.
	wingFarMin = 3
)

// wingPlan is the star whose wing may reach a spot and the stars each
// sub's own star shape is built from.
type wingPlan struct {
	g, x, y float64
	sep     float64
	epsf    [][2]float64
	// nights are the nights the wing is fitted on: the flagged ones and
	// those wingFarDays or more from every one.
	nights map[string]bool
}

// wingNightsOf are the flagged nights and the far ones among subs.
func wingNightsOf(sp *spotPlan, subs []Sub) map[string]bool {
	var flagged []time.Time
	for k := range sp.flagged {
		if d, err := time.Parse(dateLayout, k); err == nil {
			flagged = append(flagged, d)
		}
	}
	out := map[string]bool{}
	for _, s := range subs {
		k := nightKey(s.Night)
		if _, ok := out[k]; ok {
			continue
		}
		far := true
		for _, f := range flagged {
			if math.Abs(dateOnly(s.Night).Sub(f).Hours()/24) < wingFarDays {
				far = false
				break
			}
		}
		out[k] = far || sp.flagged[k]
	}
	return out
}

// planWing finds the brightest catalogued star of G under wingMaxG within
// wingMaxPx of a spot with no star, and the isolated stars nearest it;
// with fewer than epsfMin of those its wing cannot be fitted.
func (f *field) planWing(sp *spotPlan) *wingPlan {
	var w *wingPlan
	for _, i := range f.grid.Within(sp.x, sp.y, wingMaxPx) {
		s := f.pl[i]
		d := math.Hypot(s.x-sp.x, s.y-sp.y)
		if s.G >= wingMaxG || d > wingMaxPx || (w != nil && s.G >= w.g) {
			continue
		}
		w = &wingPlan{g: s.G, x: s.x, y: s.y, sep: d}
	}
	if w == nil {
		return nil
	}
	type cand struct {
		x, y, d float64
	}
	var cs []cand
	for _, c := range f.pl {
		if c.G < epsfMinG || c.G > epsfMaxG {
			continue
		}
		d := math.Hypot(c.x-sp.x, c.y-sp.y)
		if d < 2*wingMaxPx || d > epsfReach || c.x < compMargin || c.y < compMargin || c.x > float64(f.width)-compMargin || c.y > float64(f.height)-compMargin {
			continue
		}
		lonely := true
		for _, j := range f.grid.Within(c.x, c.y, float64(epsfHalf)) {
			o := f.pl[j]
			if (o.x != c.x || o.y != c.y) && o.G < c.G+shapeNbMag {
				lonely = false
				break
			}
		}
		if lonely {
			cs = append(cs, cand{c.x, c.y, d})
		}
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].d < cs[b].d })
	for _, c := range cs[:min(len(cs), epsfStars)] {
		w.epsf = append(w.epsf, [2]float64{c.x, c.y})
	}
	return w
}

// cubic is Keys' cubic interpolation weight at distance t.
func cubic(t float64) float64 {
	t = math.Abs(t)
	switch {
	case t < 1:
		return 1.5*t*t*t - 2.5*t*t + 1
	case t < 2:
		return -0.5*t*t*t + 2.5*t*t - 4*t + 2
	}
	return 0
}

// sample reads a grid of side n at (x, y) with cubic interpolation, NaN
// where any pixel it needs is missing or off the grid.
func sample(at func(x, y int) float64, x, y float64) float64 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	var s float64
	for j := -1; j <= 2; j++ {
		wy := cubic(y - float64(y0+j))
		for i := -1; i <= 2; i++ {
			v := at(x0+i, y0+j)
			if !finite(v) {
				return math.NaN()
			}
			s += cubic(x-float64(x0+i)) * wy * v
		}
	}
	return s
}

// epsf is a sub's own star shape: the median of its nearby isolated
// stars, each less its sky and scaled to unit light, sampled at whole
// pixels from their centres.
type epsf struct {
	v []float64
}

func (e epsf) grid(x, y int) float64 {
	if x < 0 || y < 0 || x >= epsfSide || y >= epsfSide {
		return 0
	}
	return e.v[y*epsfSide+x]
}

// at is the star shape dx, dy from the star's centre.
func (e epsf) at(dx, dy float64) float64 {
	if math.Abs(dx) > epsfHalf-1 || math.Abs(dy) > epsfHalf-1 {
		return 0
	}
	return sample(e.grid, dx+epsfHalf, dy+epsfHalf)
}

// buildEPSF builds a sub's star shape from stars at their places in it.
func buildEPSF(p *discoveries.Plane, stars [][2]float64) (epsf, bool) {
	at := func(x, y int) float64 {
		v := p.At(x, y)
		if isNaN32(v) {
			return math.NaN()
		}
		return float64(v)
	}
	var stamps [][]float64
	for _, s := range stars {
		st := make([]float64, epsfSide*epsfSide)
		var sky []float64
		bad := false
		for j := range epsfSide {
			for i := range epsfSide {
				v := sample(at, s[0]+float64(i-epsfHalf), s[1]+float64(j-epsfHalf))
				st[j*epsfSide+i] = v
				r := math.Hypot(float64(i-epsfHalf), float64(j-epsfHalf))
				switch {
				case !finite(v) && r <= epsfCore:
					bad = true
				case r > epsfHalf-2 && finite(v):
					sky = append(sky, v)
				}
			}
		}
		bg := median(sky)
		if bad || !finite(bg) {
			continue
		}
		var light float64
		for k := range st {
			st[k] -= bg
			if math.Hypot(float64(k%epsfSide-epsfHalf), float64(k/epsfSide-epsfHalf)) <= epsfCore && finite(st[k]) {
				light += st[k]
			}
		}
		if !(light > 0) {
			continue
		}
		for k := range st {
			st[k] /= light
		}
		stamps = append(stamps, st)
	}
	if len(stamps) < epsfMin {
		return epsf{}, false
	}
	out := epsf{v: make([]float64, epsfSide*epsfSide)}
	col := make([]float64, 0, len(stamps))
	for k := range out.v {
		col = col[:0]
		for _, st := range stamps {
			if finite(st[k]) {
				col = append(col, st[k])
			}
		}
		out.v[k] = 0
		if len(col) > 0 {
			out.v[k] = median(col)
		}
	}
	return out, true
}

// wingFit fits the star (place free within wingMove px) and a point
// source at the spot, both with the sub's own star shape, on a sloped
// sky, and gives the spot's light and its error.
func wingFit(p *discoveries.Plane, e epsf, sx, sy, px, py float64) (flux, ferr float64, ok bool) {
	mx, my := (sx+px)/2, (sy+py)/2
	reach := math.Hypot(sx-px, sy-py)/2 + wingFitR
	type pt struct{ x, y, v float64 }
	var pts []pt
	for y := int(math.Floor(my - reach)); y <= int(math.Ceil(my+reach)); y++ {
		for x := int(math.Floor(mx - reach)); x <= int(math.Ceil(mx+reach)); x++ {
			if math.Hypot(float64(x)-mx, float64(y)-my) > reach {
				continue
			}
			if v := p.At(x, y); !isNaN32(v) {
				pts = append(pts, pt{float64(x), float64(y), float64(v)})
			}
		}
	}
	const n = 5
	if len(pts) < 4*n {
		return 0, 0, false
	}
	solve := func(qx, qy float64) (coef, ata []float64, rss float64, good bool) {
		ata = make([]float64, n*n)
		atb := make([]float64, n)
		rows := make([][n]float64, len(pts))
		for i, q := range pts {
			rows[i] = [n]float64{e.at(q.x-qx, q.y-qy), e.at(q.x-px, q.y-py), 1, (q.x - mx) / reach, (q.y - my) / reach}
			for a := range n {
				atb[a] += rows[i][a] * q.v
				for b := range n {
					ata[a*n+b] += rows[i][a] * rows[i][b]
				}
			}
		}
		c, good := solveSPD(ata, atb, n)
		if !good {
			return nil, nil, 0, false
		}
		for i, q := range pts {
			var m float64
			for a := range n {
				m += rows[i][a] * c[a]
			}
			rss += (q.v - m) * (q.v - m)
		}
		return c, ata, rss, true
	}
	bx, by, bv := sx, sy, math.Inf(1)
	search := func(cx, cy, step float64, n int) {
		for j := -n; j <= n; j++ {
			for i := -n; i <= n; i++ {
				qx, qy := cx+float64(i)*step, cy+float64(j)*step
				if math.Hypot(qx-sx, qy-sy) > wingMove {
					continue
				}
				if _, _, rss, good := solve(qx, qy); good && rss < bv {
					bx, by, bv = qx, qy, rss
				}
			}
		}
	}
	search(sx, sy, 0.5, 3)
	search(bx, by, 0.1, 3)
	if math.IsInf(bv, 0) {
		return 0, 0, false
	}
	coef, ata, rss, good := solve(bx, by)
	if !good {
		return 0, 0, false
	}
	unit := make([]float64, n)
	unit[1] = 1
	z, good := solveSPD(ata, unit, n)
	if !good || !(z[1] > 0) {
		return 0, 0, false
	}
	s := math.Sqrt(rss / float64(len(pts)-n))
	return coef[1], s * math.Sqrt(z[1]), true
}

// Wing is the spot's light next to a bright star, fitted with the star
// in each sub's own star shape: on the flagged night where it is
// strongest against its level on nights at least wingFarDays away.
type Wing struct {
	G      float64 `json:"g"`
	SepPx  float64 `json:"sep_px"`
	Night  string  `json:"night,omitempty"`
	Filter string  `json:"filter,omitempty"`
	Excess float64 `json:"excess"`
	Sigma  float64 `json:"sigma"`
	// Err is the excess's error, in the spot's light units.
	Err float64 `json:"err,omitempty"`
	// Floor is the part of Err no number of subs takes away: how much the
	// wing's light at the spot moves from one far night to the next.
	Floor float64 `json:"floor,omitempty"`
	// NightErr is the error of the night's mean light from its N subs
	// alone, the part of Err more subs take away.
	NightErr float64 `json:"night_err,omitempty"`
	N        int     `json:"n,omitempty"`
	Far      int     `json:"far"`
	// Measured is false when too few subs or far nights could be fitted.
	Measured bool `json:"measured"`
}

// wingNights compares the spot's fitted light on each flagged night with
// its level on the far nights, per filter, in the filter groups the
// detection flagged it in only: 27673 (Cygnus Loop, 2026-09-02), found in
// Red, Green and Blue, was judged by its O-III frames of that night,
// which stood highest at 1.0 sigma. Too few subs in those groups leave it
// not measured.
func wingNights(subs []Sub, sp *spotPlan, e *ensemble) *Wing {
	w := sp.bare.wing
	out := &Wing{G: w.g, SepPx: w.sep}
	type key struct{ night, filter string }
	vals := map[key][]float64{}
	errs := map[key][]float64{}
	for i, r := range sp.recs {
		t, ok := e.transp[i]
		if r == nil || !r.wingOK || !ok || !(t > 0) || !sp.groupFilter(subs[i].Filter) {
			continue
		}
		k := key{nightKey(subs[i].Night), subs[i].Filter}
		vals[k] = append(vals[k], r.wing/t)
		errs[k] = append(errs[k], r.wingErr/t)
	}
	var flagged []time.Time
	for k := range sp.flagged {
		if d, err := time.Parse(dateLayout, k); err == nil {
			flagged = append(flagged, d)
		}
	}
	far := func(night string) bool {
		d, err := time.Parse(dateLayout, night)
		if err != nil {
			return false
		}
		for _, f := range flagged {
			if math.Abs(d.Sub(f).Hours()/24) < wingFarDays {
				return false
			}
		}
		return true
	}
	type level struct {
		m, e float64
		n    int
	}
	farLv := map[string][]float64{}
	nights := map[key]level{}
	for k, v := range vals {
		if len(v) < minPresent {
			continue
		}
		m, sd, n := clippedMean(v)
		e := median(errs[k]) / math.Sqrt(float64(n))
		if n >= 3 && sd/math.Sqrt(float64(n)) > e {
			e = sd / math.Sqrt(float64(n))
		}
		nights[k] = level{m, e, n}
		if far(k.night) {
			farLv[k.filter] = append(farLv[k.filter], m)
		}
	}
	best := math.Inf(-1)
	for k, lv := range nights {
		fl := farLv[k.filter]
		if !sp.flagged[k.night] || !sp.inGroup(k.night, k.filter) || len(fl) < wingFarMin {
			continue
		}
		base, spread := median(fl), robustStd(fl)
		err := math.Hypot(math.Max(lv.e, spread), spread/math.Sqrt(float64(len(fl))))
		if !(err > 0) {
			continue
		}
		s := (lv.m - base) / err
		if s > best {
			best = s
			out.Night, out.Filter, out.Excess, out.Sigma, out.Err, out.Far, out.Measured = k.night, k.filter, lv.m-base, s, err, len(fl), true
			out.Floor, out.NightErr, out.N = math.Hypot(spread, spread/math.Sqrt(float64(len(fl)))), lv.e, lv.n
		}
	}
	return out
}
