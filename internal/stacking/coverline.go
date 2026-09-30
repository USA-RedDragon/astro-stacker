package stacking

import (
	"log/slog"
	"math"
	"slices"
)

// Masked line addition for the colour cover, after the North America
// "HaMasked" rendering (/Astro/Out/north-america/claude/NOTES.md, round 2)
// and the Cygnus Loop's masked H-a and O-III: the continuum is taken out of
// the narrowband master, and the net line is added to the broadband
// channels only where a feathered mask from the line's own signal is on, so
// the sky, the dust and the stars keep their broadband colour.
//
//  1. Continuum ratio q from star photometry: stars are pure continuum, so
//     line ≈ q·cont on them. Net line = line - q·cont - sky, the sky a
//     quadratic surface fitted to the blocks without emission.
//  2. A compact mask at full resolution for knots smaller than a block (a
//     galaxy's H II regions): net line over its surroundings, well above
//     the noise and above the local continuum, which a star's colour
//     residual isn't.
//  3. How much of the broadband channel the line already is, k, from a
//     robust regression ch = k·net + b·proxy + c, the proxy a broadband
//     channel without the line (green for H-a): on star-free block medians
//     of the sky and of clear emission, or, with too few of those, on the
//     compact knots. Adding k·net again doubles the line.
//  4. An extended mask on the block grid: the smoothed net line against its
//     noise, times, for H-a, a colour gate that keeps blue continuum
//     (reflection nebulae, the lavender centre of the North America Nebula)
//     out: the line's share of red over the white-balanced blue continuum
//     must pass 0.6 before any goes in. A closing fills holes bright stars
//     leave, and a blur feathers the edges.
//
// The mask is the larger of the two, and the channel gets
// lineGain·k·mask·net.

// lineStars is the least number of stars q is measured on.
const lineStars = 10

// lineFitBlocks is the least number of blocks of clear emission k is
// fitted on; with fewer, it is fitted on the compact knots.
const lineFitBlocks = 30

// lineFitKnots is the least number of compact emission pixels k is fitted
// on; with fewer, k is lineKQDefault/q.
const lineFitKnots = 200

// lineGain multiplies the doubled line. With the cover's colour taken from
// linear ratios (colourStretch), as in the manual versions, 2 shows M31's
// and M33's H II regions and North America's fronts without neon; 4 was
// needed when each channel was stretched on its own.
const lineGain = 2.0

// Bounds on k·q, the line's response in the broadband filter relative to
// the narrowband's continuum ratio: roughly the ratio of their bandwidths,
// whatever the masters' units. Measured 0.04-0.075 on the North America
// Nebula, IC 1396, M31 and M33 covers; the manual Cygnus Loop fit gave 0.15
// ([N II] in the red filter).
const (
	lineKQMin     = 0.02
	lineKQMax     = 0.25
	lineKQDefault = 0.05
)

// Level gate of the extended mask on the smoothed net line: nothing below
// lineLevelLo noise, fully on lineLevelSpan noise above that (or the sky's
// scatter, if more).
const (
	lineLevelLo   = 4.0
	lineLevelSpan = 8.0
)

// Compact mask: the net line over its surroundings, in its noise, and as a
// share of the continuum there.
const (
	lineCompactLo        = 5.0
	lineCompactSpan      = 5.0
	lineCompactShareLo   = 0.8
	lineCompactShareSpan = 0.7
)

// Colour gate for H-a: the line's share of red over the white-balanced blue
// continuum, as in the manual version.
const (
	lineGateLo   = 0.6
	lineGateSpan = 0.4
)

// lineTarget is a channel the line is added to, with the bounds of its k
// relative to the first target's (0 for the first target itself).
type lineTarget struct {
	Data   []float32
	MaxRel float64
}

// lineInputs describes one line addition.
type lineInputs struct {
	Name  string
	Line  []float32 // narrowband plane
	Cont  []float32 // broadband channel that sees the line and its continuum
	Proxy []float32 // broadband channel without the line: continuum proxy
	Gate  []float32 // blue continuum for the colour gate; nil for none
	Into  []lineTarget
}

// addLine returns copies of the target channels with the masked net line
// added. Pixels without data (0) stay empty. When the line can't be
// measured, the channels come back unchanged.
func addLine(in lineInputs, w, h int) [][]float32 {
	out := make([][]float32, len(in.Into))
	for t, tg := range in.Into {
		out[t] = slices.Clone(tg.Data)
	}
	net, mask, ks, ok := lineMask(in, w, h)
	if !ok {
		return out
	}
	for t := range out {
		k := float32(lineGain * ks[t])
		if k <= 0 {
			continue
		}
		for i, v := range out[t] {
			if v == 0 || mask[i] == 0 || net[i] <= 0 {
				continue
			}
			out[t][i] = v + k*mask[i]*net[i]
		}
	}
	return out
}

// lineMask measures the line and builds its mask: the net line and the
// mask at full resolution, and each target's k.
func lineMask(in lineInputs, w, h int) (net, mask []float32, ks []float64, ok bool) {
	s := max(4, int(math.Round(float64(max(w, h))/200)))
	lineB, gw, gh := blockMedians(in.Line, w, h, s)
	contB, _, _ := blockMedians(in.Cont, w, h, s)
	var gateB []float64
	if in.Gate != nil {
		gateB, _, _ = blockMedians(in.Gate, w, h, s)
	}

	// 1. Continuum ratio from stars.
	planes := [][]float32{in.Line, in.Cont}
	blocks := [][]float64{lineB, contB}
	if in.Gate != nil {
		planes, blocks = append(planes, in.Gate), append(blocks, gateB)
	}
	ratios, n := starRatios(planes, blocks, w, h, s, gw, gh)
	if n < lineStars {
		slog.Debug("Too few stars to measure the line", "line", in.Name, "stars", n)
		return nil, nil, nil, false
	}
	q := ratios[0] // line/cont
	wb := 0.0      // cont/gate: white balance of the gate channel
	if in.Gate != nil {
		wb = ratios[1]
	}

	// Net line on the grid, less the sky: a smooth surface fitted to the
	// blocks without line emission, which also takes out gradients the two
	// filters don't share.
	netB := make([]float64, len(lineB))
	for i := range netB {
		netB[i] = lineB[i] - q*contB[i]
	}
	sky, skyCells := skySurface(netB, gw, gh)
	for i := range netB {
		netB[i] -= sky[i]
	}

	noise := gridNoise(netB, gw, gh)
	if noise <= 0 {
		return nil, nil, nil, false
	}

	// 2. The compact mask, at full resolution, for H II regions too small
	// for the grid (a galaxy's): the net line over its surroundings, well
	// above the noise and a good part of the continuum there, which a
	// star's colour residual isn't.
	skyUp := upsampleGrid(sky, gw, gh, s, w, h)
	net = make([]float32, w*h)
	for i := range net {
		if in.Line[i] != 0 && in.Cont[i] != 0 {
			net[i] = float32(float64(in.Line[i]) - q*float64(in.Cont[i]) - float64(skyUp[i]))
		}
	}
	netUp := upsampleGrid(netB, gw, gh, s, w, h)
	local := gaussPlane(net, w, h, 1)
	for i := range local {
		if net[i] == 0 {
			local[i] = 0
		} else {
			local[i] -= netUp[i]
		}
	}
	contLocal := localPlane(in.Cont, contB, w, h, s, gw, gh)
	_, sigmaF := statsNonZero(local)
	compact := make([]float64, w*h)
	var knots []int
	if sigmaF > 0 {
		for i, v := range local {
			if v <= 0 {
				continue
			}
			c := smoothstep((float64(v)/sigmaF - lineCompactLo) / lineCompactSpan)
			if cl := q * float64(contLocal[i]); c > 0 && cl > 0 {
				c *= smoothstep((float64(v)/cl - lineCompactShareLo) / lineCompactShareSpan)
			}
			compact[i] = c
			if c > 0.5 {
				knots = append(knots, i)
			}
		}
		// Grown by two pixels before feathering, so a knot a few pixels
		// across keeps its full weight.
		compact = gaussGrid(dilateGrid(compact, w, h, 2), w, h, 2.5)
	}

	// 3. The line's share of each target channel. Fitted on the sky and
	// the blocks of clear emission, where the line is most of the
	// narrowband's signal: elsewhere the net line varies with the colour
	// of the continuum (a galaxy's old stars), not with the line. Without
	// enough of those, on the compact knots over their surroundings.
	fitCells := slices.Clone(skyCells)
	emission := 0
	contSky := skyLevel(contB)
	for i, v := range netB {
		if v > 3*noise && v > q*(contB[i]-contSky) {
			fitCells[i] = true
			emission++
		}
	}
	proxyB, _, _ := blockMedians(in.Proxy, w, h, s)
	var proxyK, netK []float64
	if len(knots) >= lineFitKnots {
		proxyLocal := localPlane(in.Proxy, proxyB, w, h, s, gw, gh)
		for _, i := range knots {
			proxyK = append(proxyK, float64(proxyLocal[i]))
			netK = append(netK, float64(local[i]))
		}
	}
	ks = make([]float64, len(in.Into))
	how := "default"
	for t, tg := range in.Into {
		chB, _, _ := blockMedians(tg.Data, w, h, s)
		var k float64
		fit := false
		if emission >= lineFitBlocks {
			if k, fit = regressLine(chB, netB, proxyB, fitCells); fit && t == 0 {
				how = "blocks"
			}
		}
		if !fit && netK != nil {
			chLocal := localPlane(tg.Data, chB, w, h, s, gw, gh)
			chK := make([]float64, len(knots))
			for j, i := range knots {
				chK[j] = float64(chLocal[i])
			}
			if k, fit = regressLine(chK, netK, proxyK, nil); fit && t == 0 {
				how = "knots"
			}
		}
		lo, hi := lineKQMin/q, lineKQMax/q
		if t > 0 {
			lo, hi = 0, tg.MaxRel*ks[0]
		}
		if !fit && t == 0 {
			k = lineKQDefault / q
		}
		ks[t] = math.Max(lo, math.Min(hi, k))
	}

	// 4. The extended mask, on the grid: the smoothed net line against the
	// noise, times the colour gate. The level gate starts at lineLevelLo
	// times the noise and is fully on lineLevelSpan noise above that, or
	// the scatter of the sky's net line (faint emission and dust among the
	// North America Nebula's dark clouds), whichever is more.
	sm := gaussGrid(netB, gw, gh, 1)
	sigma := noise * 0.28 // σ of a σ=1 Gaussian mean
	var skyRes []float64
	for i, v := range sm {
		if skyCells[i] && !math.IsNaN(v) {
			skyRes = append(skyRes, math.Abs(v))
		}
	}
	span := lineLevelSpan * sigma
	if len(skyRes) > 0 {
		slices.Sort(skyRes)
		span = max(span, skyRes[len(skyRes)/2]*madToSigma)
	}
	var gs []float64
	var gzero float64
	if in.Gate != nil {
		gs = gaussGrid(gateB, gw, gh, 1)
		gzero = skyLevel(gateB)
	}
	m := make([]float64, len(sm))
	for i, v := range sm {
		if math.IsNaN(v) {
			continue
		}
		g := smoothstep((v - lineLevelLo*sigma) / span)
		if in.Gate != nil && g > 0 {
			if blue := (gs[i] - gzero) * wb; blue > 0 {
				g *= smoothstep((ks[0]*v/blue - lineGateLo) / lineGateSpan)
			}
		}
		m[i] = g
	}
	// A closing fills holes bright stars leave; a blur feathers the edge.
	r := max(1, int(math.Round(float64(gw)/60)))
	m = erodeGrid(dilateGrid(m, gw, gh, r), gw, gh, r)
	m = gaussGrid(m, gw, gh, 2)
	mask = upsampleGrid(m, gw, gh, s, w, h)

	var on float64
	for i := range mask {
		if net[i] == 0 {
			mask[i] = 0
			continue
		}
		mask[i] = max(0, min(1, max(mask[i], float32(compact[i]))))
		on += float64(mask[i])
	}
	slog.Debug("Line mask", "line", in.Name, "stars", n, "q", q, "wb", wb, "k", ks, "fit", how,
		"emission", emission, "knots", len(knots), "sigma", sigma, "span", span, "sigmaF", sigmaF,
		"masked", on/float64(len(mask)), "block", s)
	return net, mask, ks, true
}

// localPlane is p over its surroundings: p blurred by a pixel, less its
// block medians interpolated. Empty pixels stay 0.
func localPlane(p []float32, blocks []float64, w, h, s, gw, gh int) []float32 {
	up := upsampleGrid(blocks, gw, gh, s, w, h)
	out := gaussPlane(p, w, h, 1)
	for i := range out {
		if p[i] == 0 {
			out[i] = 0
		} else {
			out[i] -= up[i]
		}
	}
	return out
}

// smoothstep is 0 below 0, 1 above 1, and a smooth step between.
func smoothstep(x float64) float64 {
	x = math.Max(0, math.Min(1, x))
	return x * x * (3 - 2*x)
}

// blockMedians is the median of each s×s block, NaN for blocks less than
// half covered. Stars, a few pixels across, don't move it.
func blockMedians(p []float32, w, h, s int) ([]float64, int, int) {
	gw, gh := (w+s-1)/s, (h+s-1)/s
	out := make([]float64, gw*gh)
	buf := make([]float64, 0, s*s)
	for gy := range gh {
		for gx := range gw {
			buf = buf[:0]
			for y := gy * s; y < min(h, (gy+1)*s); y++ {
				for x := gx * s; x < min(w, (gx+1)*s); x++ {
					if v := p[y*w+x]; v != 0 && !math.IsNaN(float64(v)) {
						buf = append(buf, float64(v))
					}
				}
			}
			if len(buf) < s*s/2 {
				out[gy*gw+gx] = math.NaN()
				continue
			}
			slices.Sort(buf)
			out[gy*gw+gx] = buf[len(buf)/2]
		}
	}
	return out, gw, gh
}

// upsampleGrid interpolates a block grid bilinearly to w×h pixels, block
// centres at (i+½)·s. NaN blocks count as 0.
func upsampleGrid(g []float64, gw, gh, s, w, h int) []float32 {
	at := func(x, y int) float64 {
		x, y = max(0, min(gw-1, x)), max(0, min(gh-1, y))
		if v := g[y*gw+x]; !math.IsNaN(v) {
			return v
		}
		return 0
	}
	out := make([]float32, w*h)
	for y := range h {
		fy := (float64(y)+0.5)/float64(s) - 0.5
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		for x := range w {
			fx := (float64(x)+0.5)/float64(s) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			v := (1-ty)*((1-tx)*at(x0, y0)+tx*at(x0+1, y0)) + ty*((1-tx)*at(x0, y0+1)+tx*at(x0+1, y0+1))
			out[y*w+x] = float32(v)
		}
	}
	return out
}

// starRatios measures stars on the first two planes (and a third, if
// given) above their block background: the median of plane0/plane1 and of
// plane1/plane2 over 5×5 apertures on unsaturated stars found in plane1.
func starRatios(planes [][]float32, blocks [][]float64, w, h, s, gw, gh int) ([]float64, int) {
	hp := make([][]float32, len(planes))
	for k, p := range planes {
		bg := upsampleGrid(blocks[k], gw, gh, s, w, h)
		hp[k] = make([]float32, w*h)
		for i, v := range p {
			if v != 0 {
				hp[k][i] = v - bg[i]
			}
		}
	}
	ref := hp[1]
	_, sigma := statsNonZero(ref)
	if sigma <= 0 {
		return nil, 0
	}
	peak := make([]float32, len(planes))
	for k, p := range planes {
		for _, v := range p {
			peak[k] = max(peak[k], v)
		}
	}
	type star struct {
		flux   float64
		ratios []float64
	}
	var stars []star
	const r = 2
	for y := r + 1; y < h-r-1; y++ {
		for x := r + 1; x < w-r-1; x++ {
			i := y*w + x
			v := ref[i]
			if float64(v) < 15*sigma {
				continue
			}
			isMax, bad := true, false
			sums := make([]float64, len(planes))
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					j := i + dy*w + dx
					if (dx != 0 || dy != 0) && ref[j] >= v {
						isMax = false
					}
					for k, p := range planes {
						if p[j] == 0 || p[j] > 0.5*peak[k] {
							bad = true
						}
						sums[k] += float64(hp[k][j])
					}
				}
			}
			if !isMax || bad || sums[1] <= 0 {
				continue
			}
			st := star{flux: sums[1]}
			st.ratios = append(st.ratios, sums[0]/sums[1])
			if len(planes) > 2 {
				if sums[2] <= 0 {
					continue
				}
				st.ratios = append(st.ratios, sums[1]/sums[2])
			}
			stars = append(stars, st)
		}
	}
	// The brightest may be saturated in the full master. Of the rest, the
	// brighter quarter: faint stars in the Milky Way are far and reddened
	// (North America Nebula: faint stars B/R 0.45, bright ones about 1).
	slices.SortFunc(stars, func(a, b star) int { return int(math.Copysign(1, a.flux-b.flux)) })
	stars = stars[:len(stars)-len(stars)/20]
	if len(stars) >= 4*lineStars {
		stars = stars[len(stars)*3/4:]
	}
	if len(stars) == 0 {
		return nil, 0
	}
	out := make([]float64, len(planes)-1)
	for k := range out {
		v := make([]float64, len(stars))
		for i, st := range stars {
			v[i] = st.ratios[k]
		}
		slices.Sort(v)
		out[k] = v[len(v)/2]
	}
	if out[0] <= 0 {
		return nil, 0
	}
	return out, len(stars)
}

// skyLevel is the level of the emptiest sky on a block grid: the median of
// the lowest fifth of the blocks, which is the sky's own median in a field
// of little signal and the darkest parts in one full of nebula.
func skyLevel(g []float64) float64 {
	v := make([]float64, 0, len(g))
	for _, x := range g {
		if !math.IsNaN(x) {
			v = append(v, x)
		}
	}
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/10]
}

// skySurface fits a quadratic surface to the blocks free of line emission:
// starting from the lower half, it refits to the blocks within -3σ and +2σ
// of the last fit until they settle. It returns the surface on every block
// and which blocks it was fitted to.
func skySurface(g []float64, gw, gh int) ([]float64, []bool) {
	return skySurfaceAmong(g, nil, gw, gh)
}

// skySurfaceAmong is skySurface fitted only among the given blocks, all of
// them to start with; nil means from the lower half of all.
func skySurfaceAmong(g []float64, among []bool, gw, gh int) ([]float64, []bool) {
	basis := func(i int) [6]float64 {
		x := 2*(float64(i%gw)+0.5)/float64(gw) - 1
		y := 2*(float64(i/gw)+0.5)/float64(gh) - 1
		return [6]float64{1, x, y, x * x, x * y, y * y}
	}
	var valid []float64
	for _, v := range g {
		if !math.IsNaN(v) {
			valid = append(valid, v)
		}
	}
	surface := make([]float64, len(g))
	keep := make([]bool, len(g))
	if len(valid) == 0 {
		return surface, keep
	}
	slices.Sort(valid)
	med := valid[len(valid)/2]
	for i, v := range g {
		if among != nil {
			keep[i] = !math.IsNaN(v) && among[i]
		} else {
			keep[i] = !math.IsNaN(v) && v <= med
		}
	}
	var coef [6]float64
	for range 20 {
		var a [6][6]float64
		var b [6]float64
		n := 0
		for i, v := range g {
			if !keep[i] {
				continue
			}
			x := basis(i)
			for r := range 6 {
				for c := range 6 {
					a[r][c] += x[r] * x[c]
				}
				b[r] += x[r] * v
			}
			n++
		}
		c, ok := solve6(a, b)
		if !ok || n < 12 {
			break
		}
		coef = c
		var res []float64
		for i, v := range g {
			if keep[i] {
				res = append(res, math.Abs(v-dot6(coef, basis(i))))
			}
		}
		slices.Sort(res)
		sd := res[len(res)/2] * madToSigma
		changed := false
		for i, v := range g {
			k := false
			if !math.IsNaN(v) && (among == nil || among[i]) {
				d := v - dot6(coef, basis(i))
				k = d > -3*sd && d < 2*sd
			}
			changed = changed || k != keep[i]
			keep[i] = k
		}
		if !changed {
			break
		}
	}
	for i := range g {
		surface[i] = dot6(coef, basis(i))
	}
	return surface, keep
}

func dot6(a, b [6]float64) float64 {
	var s float64
	for i := range 6 {
		s += a[i] * b[i]
	}
	return s
}

// solve6 solves a·x = b by Gaussian elimination with partial pivoting.
func solve6(a [6][6]float64, b [6]float64) ([6]float64, bool) {
	var x [6]float64
	for c := range 6 {
		p := c
		for r := c + 1; r < 6; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		if a[p][c] == 0 || math.IsNaN(a[p][c]) {
			return x, false
		}
		a[c], a[p] = a[p], a[c]
		b[c], b[p] = b[p], b[c]
		for r := c + 1; r < 6; r++ {
			f := a[r][c] / a[c][c]
			for k := c; k < 6; k++ {
				a[r][k] -= f * a[c][k]
			}
			b[r] -= f * b[c]
		}
	}
	for r := 5; r >= 0; r-- {
		s := b[r]
		for k := r + 1; k < 6; k++ {
			s -= a[r][k] * x[k]
		}
		x[r] = s / a[r][r]
	}
	return x, true
}

// gaussPlane blurs a full-resolution plane with a Gaussian of σ pixels.
func gaussPlane(p []float32, w, h int, sigma float64) []float32 {
	r := int(math.Ceil(3 * sigma))
	k := make([]float32, 2*r+1)
	var sum float32
	for i := range k {
		d := float64(i - r)
		k[i] = float32(math.Exp(-d * d / (2 * sigma * sigma)))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, len(p))
	out := make([]float32, len(p))
	for y := range h {
		row := p[y*w : (y+1)*w]
		for x := range w {
			var v float32
			for i, kv := range k {
				v += kv * row[max(0, min(w-1, x+i-r))]
			}
			tmp[y*w+x] = v
		}
	}
	for y := range h {
		for x := range w {
			var v float32
			for i, kv := range k {
				v += kv * tmp[max(0, min(h-1, y+i-r))*w+x]
			}
			out[y*w+x] = v
		}
	}
	return out
}

// gridNoise is the block-to-block noise of a grid, from the MAD of the
// differences between neighbours, which structure larger than a block
// hardly adds to.
func gridNoise(g []float64, gw, gh int) float64 {
	var d []float64
	for y := range gh {
		for x := 0; x+1 < gw; x++ {
			a, b := g[y*gw+x], g[y*gw+x+1]
			if !math.IsNaN(a) && !math.IsNaN(b) {
				d = append(d, math.Abs(a-b))
			}
		}
	}
	if len(d) == 0 {
		return 0
	}
	slices.Sort(d)
	return d[len(d)/2] * madToSigma / math.Sqrt2
}

// regressLine fits ch = k·net + b·proxy + c on the blocks in use, rejecting
// blocks off by more than 3σ, and reports whether k was measured: fitted,
// positive and more than 3σ from 0.
func regressLine(ch, net, proxy []float64, cells []bool) (float64, bool) {
	use := make([]bool, len(ch))
	for i := range ch {
		use[i] = (cells == nil || cells[i]) && !math.IsNaN(ch[i]) && !math.IsNaN(net[i]) && !math.IsNaN(proxy[i])
	}
	var coef [3]float64
	var errK float64
	for range 5 {
		var a [3][3]float64
		var y [3]float64
		n := 0
		for i := range ch {
			if !use[i] {
				continue
			}
			x := [3]float64{net[i], proxy[i], 1}
			for r := range 3 {
				for c := range 3 {
					a[r][c] += x[r] * x[c]
				}
				y[r] += x[r] * ch[i]
			}
			n++
		}
		inv, ok := invert3(a)
		if !ok || n < 10 {
			return 0, false
		}
		for r := range 3 {
			coef[r] = inv[r][0]*y[0] + inv[r][1]*y[1] + inv[r][2]*y[2]
		}
		var res []float64
		for i := range ch {
			if use[i] {
				res = append(res, math.Abs(ch[i]-coef[0]*net[i]-coef[1]*proxy[i]-coef[2]))
			}
		}
		slices.Sort(res)
		sd := res[len(res)/2] * madToSigma
		errK = sd * math.Sqrt(math.Max(0, inv[0][0]))
		if sd == 0 {
			break
		}
		for i := range ch {
			if use[i] && math.Abs(ch[i]-coef[0]*net[i]-coef[1]*proxy[i]-coef[2]) > 3*sd {
				use[i] = false
			}
		}
	}
	return coef[0], coef[0] > 0 && coef[0] > 3*errK
}

func invert3(m [3][3]float64) ([3][3]float64, bool) {
	var inv [3][3]float64
	det := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if det == 0 || math.IsNaN(det) {
		return inv, false
	}
	for r := range 3 {
		for c := range 3 {
			r1, r2 := (c+1)%3, (c+2)%3
			c1, c2 := (r+1)%3, (r+2)%3
			inv[r][c] = (m[r1][c1]*m[r2][c2] - m[r1][c2]*m[r2][c1]) / det
		}
	}
	return inv, true
}

// gaussGrid blurs a grid with a Gaussian of σ cells, ignoring NaN cells,
// which stay NaN.
func gaussGrid(g []float64, gw, gh int, sigma float64) []float64 {
	r := int(math.Ceil(3 * sigma))
	k := make([]float64, 2*r+1)
	for i := range k {
		d := float64(i - r)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
	}
	pass := func(src []float64, dx, dy int) []float64 {
		dst := make([]float64, len(src))
		for y := range gh {
			for x := range gw {
				if math.IsNaN(src[y*gw+x]) {
					dst[y*gw+x] = math.NaN()
					continue
				}
				var sum, wt float64
				for i, kv := range k {
					xx, yy := x+(i-r)*dx, y+(i-r)*dy
					if xx < 0 || yy < 0 || xx >= gw || yy >= gh {
						continue
					}
					if v := src[yy*gw+xx]; !math.IsNaN(v) {
						sum += kv * v
						wt += kv
					}
				}
				dst[y*gw+x] = sum / wt
			}
		}
		return dst
	}
	return pass(pass(g, 1, 0), 0, 1)
}

// dilateGrid and erodeGrid take the maximum and minimum over a square of
// radius r cells.
func dilateGrid(g []float64, gw, gh, r int) []float64 { return morphGrid(g, gw, gh, r, math.Max) }
func erodeGrid(g []float64, gw, gh, r int) []float64  { return morphGrid(g, gw, gh, r, math.Min) }

func morphGrid(g []float64, gw, gh, r int, f func(a, b float64) float64) []float64 {
	pass := func(src []float64, dx, dy int) []float64 {
		dst := make([]float64, len(src))
		for y := range gh {
			for x := range gw {
				v := src[y*gw+x]
				for i := -r; i <= r; i++ {
					xx, yy := x+i*dx, y+i*dy
					if xx >= 0 && yy >= 0 && xx < gw && yy < gh {
						v = f(v, src[yy*gw+xx])
					}
				}
				dst[y*gw+x] = v
			}
		}
		return dst
	}
	return pass(pass(g, 1, 0), 0, 1)
}
