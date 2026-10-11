package stacking

import (
	"math"
	"slices"
)

// shadowCell is the side of the cells subs are compared in, in pixels.
const shadowCell = 64

// shadowDepth is the shallowest deficit, as a share of the sub's sky, filled
// in. Haze and gradients stay well under it once the large scale is taken
// off; a shadow the cover's stretch shows as a patch is above it.
const shadowDepth = 0.015

// shadowSigmas sets a noisier sub's threshold: a deficit must also be this
// many times the spread of its smoothed cells.
const shadowSigmas = 5

// shadowMinCells and shadowMaxCells bound a shadow's size, about 250 to 800
// px across. Dust or dew on the camera window, about a centimetre in front
// of the sensor, casts a disc 500 px across at f/5.4; smaller deficits are
// the target's own detail, and on a nebula haze differs between subs over
// far larger areas.
const (
	shadowMinCells = 12
	shadowMaxCells = 120
)

// shadowRound is the least share of the disc around a shadow it fills: the
// out-of-focus image of a speck is round.
const shadowRound = 0.4

// shadowSignal is the most a cell may be brighter than the sky around it,
// as a share of the sky, and still be compared: a sub at half the others'
// transparency is then short by less than shadowDepth.
const shadowSignal = 0.02

// shadowSpan is the side of the window, in cells, whose median is the large
// scale taken off: wider than a shadow, so its median stays on the sky.
const shadowSpan = 21

// shadowGrow is how far past a shadow its sub is filled in, in shadow
// radii, so the fill takes in its soft edge.
const shadowGrow = 1.0

// shadowMinSubs is the fewest subs whose cells a shadow is found against.
const shadowMinSubs = 3

// cellMedians is the level of a sub's samples in each shadowCell cell: the
// mean of those within 3σ of their median, from every other row and
// column, so stars don't count. It is NaN where a tenth of them hold no
// data, and next to such cells or the frame's edge: at the edge of a sub's
// frame the registration blends in what lies outside. get returns sample i
// normalized and whether it holds data.
func cellMedians(w, h int, get func(i int) (float32, bool)) []float32 {
	gw, gh := (w+shadowCell-1)/shadowCell, (h+shadowCell-1)/shadowCell
	out := make([]float32, gw*gh)
	vals := make([]float32, 0, shadowCell*shadowCell/4)
	for cy := range gh {
		for cx := range gw {
			vals = vals[:0]
			x1, y1 := min((cx+1)*shadowCell, w), min((cy+1)*shadowCell, h)
			total := 0
			for y := cy * shadowCell; y < y1; y += 2 {
				for x := cx * shadowCell; x < x1; x += 2 {
					total++
					if v, ok := get(y*w + x); ok {
						vals = append(vals, v)
					}
				}
			}
			if len(vals) < total*9/10 {
				out[cy*gw+cx] = float32(math.NaN())
				continue
			}
			out[cy*gw+cx] = clippedMean(vals)
		}
	}
	g := cellGrid{w: w, gw: gw, gh: gh}
	edge := make([]bool, len(out))
	for i, v := range out {
		if x, y := i%gw, i/gw; math.IsNaN(float64(v)) || x == 0 || y == 0 || x == gw-1 || y == gh-1 {
			g.around(i, 1, func(j int) { edge[j] = true })
		}
	}
	for i := range out {
		if edge[i] {
			out[i] = float32(math.NaN())
		}
	}
	return out
}

// clippedMean is the mean of v within 3σ of its median; it reorders v.
func clippedMean(v []float32) float32 {
	m := median32(v)
	dev := make([]float32, len(v))
	for i, x := range v {
		dev[i] = abs32(x - m)
	}
	limit := 3 * madToSigma * median32(dev)
	s, n := 0.0, 0
	for _, x := range v {
		if abs32(x-m) <= limit {
			s += float64(x)
			n++
		}
	}
	if n == 0 {
		return m
	}
	return float32(s / float64(n))
}

// shadow is a deficit found in a sub: its depth as a share of the sky, its
// deepest point in pixels, and how many cells it covers.
type shadow struct {
	depth float64
	x, y  int
	cells int
}

// shadowFill fills a sub's shadows in. A sub can carry a soft dark disc
// that the flat doesn't: dew or frost on the camera window for part of a
// night, or dust that came and went between flats. On Bode's Galaxy, the
// subs of 2025-11-21/22 lost 3% of their light in a disc 500 px across that
// deepened through the night with the humidity, fixed on the sensor at
// every rotator angle and gone the next evening. Per pixel it is far below
// the noise, so per-pixel rejection keeps it, but over a few hundred pixels
// it is coherent, and the master carries it in proportion to the sub's
// weight. In cells, against the median of the other subs, it stands out,
// and the sub is brought up there to the others' level around it.
//
// Only deficits are looked for: anything on the sensor side the flat missed
// takes light away, while a flat's own fault is in every sub calibrated with
// it, which a comparison between subs can't see.
type shadowFill struct {
	w, gw, gh int
	// corr is each cell's fill as a share of the sub's sky; near marks the
	// cells whose pixels get some of it.
	corr    []float64
	near    []bool
	shadows []shadow
}

// at is pixel i's fill as a share of the sub's sky, interpolated between
// cell centres.
func (f *shadowFill) at(i int) float64 {
	x, y := i%f.w, i/f.w
	if !f.near[(y/shadowCell)*f.gw+x/shadowCell] {
		return 0
	}
	fx := (float64(x)+0.5)/shadowCell - 0.5
	fy := (float64(y)+0.5)/shadowCell - 0.5
	x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
	tx, ty := fx-float64(x0), fy-float64(y0)
	c := func(xx, yy int) float64 {
		return f.corr[min(max(yy, 0), f.gh-1)*f.gw+min(max(xx, 0), f.gw-1)]
	}
	return (1-ty)*((1-tx)*c(x0, y0)+tx*c(x0+1, y0)) + ty*((1-tx)*c(x0, y0+1)+tx*c(x0+1, y0+1))
}

// findShadows compares each sub's cellMedians with the median of the other
// subs' and returns its fill, nil for a sub without shadows. skies are the
// subs' sky levels in the units of their cells.
func findShadows(cells [][]float32, skies []float64, w, h int) []*shadowFill {
	fills := make([]*shadowFill, len(cells))
	if len(cells) < shadowMinSubs {
		return fills
	}
	g := cellGrid{w: w, gw: (w + shadowCell - 1) / shadowCell, gh: (h + shadowCell - 1) / shadowCell}
	n := g.gw * g.gh
	others := make([]float32, 0, len(cells))
	for j, own := range cells {
		sky := skies[j]
		if !(sky > 0) || len(own) != n {
			continue
		}
		r := make([]float64, n)
		level := make([]float64, n)
		valid := make([]bool, n)
		for k := range n {
			if math.IsNaN(float64(own[k])) {
				continue
			}
			others = others[:0]
			for o, c := range cells {
				if o != j && len(c) == n && !math.IsNaN(float64(c[k])) {
					others = append(others, c[k])
				}
			}
			if len(others) < shadowMinSubs-1 {
				continue
			}
			m := float64(median32(others))
			r[k], level[k], valid[k] = (float64(own[k])-m)/sky, m/sky, true
		}
		fills[j] = g.fill(r, level, valid)
	}
	return fills
}

type cellGrid struct{ w, gw, gh int }

// fill finds the shadows in a sub's cells, r its difference from the
// others and level theirs, both as shares of its sky. A hazy sub is short
// of the target by its transparency, which is no shadow: that is fitted
// out over the frame, and only cells about as bright as the sky around
// them are compared.
func (g cellGrid) fill(r, level []float64, valid []bool) *shadowFill {
	k := clippedSlope(r, level, valid)
	for i := range r {
		r[i] -= k * level[i]
	}
	signal := g.highPass(level, valid)
	for i := range valid {
		valid[i] = valid[i] && signal[i] <= shadowSignal
	}
	hp := g.highPass(r, valid)
	sm, limit, ok := g.smooth(hp, valid)
	if !ok {
		return nil
	}
	low := make([]bool, len(r))
	for i := range low {
		low[i] = valid[i] && sm[i] < -limit
	}
	in := make([]bool, len(r))
	var found []shadow
	for _, comp := range g.components(low) {
		if len(comp) < shadowMinCells || len(comp) > shadowMaxCells || !g.round(comp) {
			continue
		}
		deepest := comp[0]
		for _, c := range comp {
			if sm[c] < sm[deepest] {
				deepest = c
			}
		}
		found = append(found, shadow{depth: -sm[deepest], cells: len(comp),
			x: (deepest%g.gw)*shadowCell + shadowCell/2, y: (deepest/g.gw)*shadowCell + shadowCell/2})
		g.grow(comp, in)
	}
	if len(found) == 0 {
		return nil
	}
	corr := g.spread(hp, valid, in)
	near := make([]bool, len(r))
	for i, on := range in {
		if on {
			g.around(i, 1, func(j int) { near[j] = true })
		}
	}
	return &shadowFill{w: g.w, gw: g.gw, gh: g.gh, corr: corr, near: near, shadows: found}
}

// smooth averages hp over each valid cell's valid neighbours and returns
// the threshold a deficit must pass; ok is false with too few cells.
func (g cellGrid) smooth(hp []float64, valid []bool) (sm []float64, limit float64, ok bool) {
	sm = make([]float64, len(hp))
	var spread []float64
	for i := range hp {
		if !valid[i] {
			continue
		}
		s, c := 0.0, 0
		g.around(i, 1, func(j int) {
			if valid[j] {
				s += hp[j]
				c++
			}
		})
		sm[i] = s / float64(c)
		spread = append(spread, math.Abs(sm[i]))
	}
	if len(spread) < 4*shadowMinCells {
		return nil, 0, false
	}
	slices.Sort(spread)
	return sm, max(shadowDepth, shadowSigmas*madToSigma*spread[len(spread)/2]), true
}

// components groups the marked cells that touch side by side.
func (g cellGrid) components(marked []bool) [][]int {
	var out [][]int
	seen := make([]bool, len(marked))
	for i, on := range marked {
		if !on || seen[i] {
			continue
		}
		comp := []int{i}
		seen[i] = true
		for q := 0; q < len(comp); q++ {
			x, y := comp[q]%g.gw, comp[q]/g.gw
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				xx, yy := x+d[0], y+d[1]
				if xx < 0 || yy < 0 || xx >= g.gw || yy >= g.gh {
					continue
				}
				if j := yy*g.gw + xx; marked[j] && !seen[j] {
					seen[j] = true
					comp = append(comp, j)
				}
			}
		}
		out = append(out, comp)
	}
	return out
}

// grow marks in the cells within shadowGrow radii of comp.
func (g cellGrid) grow(comp []int, in []bool) {
	rad := max(1, int(math.Round(shadowGrow*math.Sqrt(float64(len(comp))/math.Pi))))
	for _, c := range comp {
		g.around(c, rad, func(j int) {
			dx, dy := j%g.gw-c%g.gw, j/g.gw-c/g.gw
			if dx*dx+dy*dy <= rad*rad {
				in[j] = true
			}
		})
	}
}

// spread is the fill of the cells in: their deficit where measured, and
// for a cell on a star or the target the mean of its filled neighbours.
func (g cellGrid) spread(hp []float64, valid, in []bool) []float64 {
	corr := make([]float64, len(hp))
	set := make([]bool, len(hp))
	for i := range hp {
		if in[i] && valid[i] {
			corr[i], set[i] = -hp[i], true
		}
	}
	for range g.gw + g.gh {
		next := slices.Clone(set)
		changed := false
		for i := range hp {
			if !in[i] || set[i] {
				continue
			}
			s, c := 0.0, 0
			g.around(i, 1, func(j int) {
				if in[j] && set[j] {
					s += corr[j]
					c++
				}
			})
			if c > 0 {
				corr[i], next[i], changed = s/float64(c), true, true
			}
		}
		set = next
		if !changed {
			break
		}
	}
	return corr
}

// around calls f for each cell within r cells of cell i, in a square.
func (g cellGrid) around(i, r int, f func(j int)) {
	x, y := i%g.gw, i/g.gw
	for yy := max(0, y-r); yy <= min(g.gh-1, y+r); yy++ {
		for xx := max(0, x-r); xx <= min(g.gw-1, x+r); xx++ {
			f(yy*g.gw + xx)
		}
	}
}

// round reports whether comp fills at least shadowRound of the disc around
// its centre that reaches its farthest cell.
func (g cellGrid) round(comp []int) bool {
	var cx, cy float64
	for _, c := range comp {
		cx += float64(c % g.gw)
		cy += float64(c / g.gw)
	}
	cx /= float64(len(comp))
	cy /= float64(len(comp))
	r := 0.0
	for _, c := range comp {
		r = max(r, math.Hypot(float64(c%g.gw)-cx, float64(c/g.gw)-cy))
	}
	return float64(len(comp)) >= shadowRound*math.Pi*(r+0.5)*(r+0.5)
}

// highPass is v less its median over the valid cells within shadowSpan.
func (g cellGrid) highPass(v []float64, valid []bool) []float64 {
	hp := make([]float64, len(v))
	win := make([]float64, 0, shadowSpan*shadowSpan)
	for i := range v {
		if !valid[i] {
			continue
		}
		win = win[:0]
		g.around(i, shadowSpan/2, func(j int) {
			if valid[j] {
				win = append(win, v[j])
			}
		})
		slices.Sort(win)
		hp[i] = v[i] - win[len(win)/2]
	}
	return hp
}

// clippedSlope is the slope of y against x over the valid cells, fitted
// with outliers clipped; 0 when x doesn't vary.
func clippedSlope(y, x []float64, valid []bool) float64 {
	use := slices.Clone(valid)
	k := 0.0
	res := make([]float64, 0, len(y))
	for range 4 {
		var sx, sy, sxx, sxy, n float64
		for i := range y {
			if use[i] {
				sx += x[i]
				sy += y[i]
				sxx += x[i] * x[i]
				sxy += x[i] * y[i]
				n++
			}
		}
		d := n*sxx - sx*sx
		if n < 3 || !(d > 0) {
			return k
		}
		k = (n*sxy - sx*sy) / d
		c := (sy - k*sx) / n
		res = res[:0]
		for i := range y {
			if valid[i] {
				res = append(res, math.Abs(y[i]-k*x[i]-c))
			}
		}
		slices.Sort(res)
		limit := 3 * madToSigma * res[len(res)/2]
		for i := range y {
			use[i] = valid[i] && math.Abs(y[i]-k*x[i]-c) <= limit
		}
	}
	return k
}
