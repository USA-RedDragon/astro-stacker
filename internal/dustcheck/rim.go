package dustcheck

import (
	"math"
	"slices"
)

// Rim streaks: on moonlit nights light scattered in from the side lights
// the dust on the camera window at a slant, and a calibrated sub carries a
// narrow dark streak, about 30 px wide and a few hundred long, along one
// side of a dust shadow's rim that its flat doesn't. On 2026-10-22, under
// a 90% Moon 32° away, it was 0.3% to 1.0% of the sky deep in Sculptor and
// M 15 subs; on 2026-10-17, the Moon 70° from Sculptor, nothing beyond
// 0.3% stood out. In Triangulum it lay on the galaxy's arms, where it
// can't be told from them.
const (
	// RimSectors is how many sectors the rim is cut into around the
	// shadow's centre; a streak spans one or two.
	RimSectors = 12
	// RimMinDiameter and RimMinPeak pick the shadows whose rims are
	// measured: window dust, not small flat-field dimples. The streaks of
	// 2026-10-22 lay on shadows 1.5% and 0.42% deep; on the kept lights of
	// 2026-10-13 to 2026-10-23 the rims of shadows 0.32% to 0.35% deep
	// dipped on the Garlic Nebula and Andromeda at sides that changed from
	// light to light.
	RimMinDiameter = 400
	RimMinPeak     = -0.4
	// RimMinZ is the least Z a streak needs to be told from a sector's
	// noise or the target's own structure.
	RimMinZ = 3.0
	// RimMinSigma is the floor of a sub's rim noise, in percent of the
	// sky: the quantization and star leftovers of the cleanest subs.
	RimMinSigma = 0.05
	// RimMaxStructure is the most the target's own structure may vary in a
	// sector of a shadow's rim, in percent of the sky (rimStructure), for
	// the sector to be measured. Over the kept lights of 2026-10-13 to
	// 2026-10-23, whole rims on empty sky varied 0.08% to 0.46% (Sculptor,
	// M 15, the Sagittarius star cloud) and rims on a galaxy or nebula 0.50%
	// (Garlic Nebula) to 3.2% (Sharpless 216, Andromeda, Cygnus Loop); the
	// sectors of Triangulum's double shadow that its spiral arms cross vary
	// 0.6% to 4.9%, and their dust lanes dip like a streak.
	RimMaxStructure = 0.45
	// RimSideSpread is how far from the sensor's +x direction, in degrees,
	// a streak's sector may lie. Every streak of 2026-10-22, on Sculptor,
	// M 15 (turned 180° on the sky against the others) and Triangulum under
	// different Moon angles, lay at 0° or ±30°, as did the four of
	// 2026-10-18 on the Sagittarius star cloud: the light comes from a side
	// fixed in the camera, not on the sky. Dips in other sectors are the
	// target's structure or noise, and only set the baseline.
	RimSideSpread = 45.0
)

// Rim band geometry, in map pixels (BinFactor sensor pixels): the band a
// streak darkens, and the gaps and widths of the references inside and
// outside it.
const (
	rimBand     = 8
	rimGap      = 8
	rimRef      = 12
	rimInner    = 0.6
	rimOuter    = 1.1
	rimMinBand  = 30
	rimMinRef   = 20
	rimMinValid = RimSectors * 3 / 4
	// rimMinStructure is the fewest template pixels a sector's structure
	// is measured from.
	rimMinStructure = 200
)

// Rim is the deepest rim streak found in a sub.
type Rim struct {
	// X, Y and Diameter are the shadow's, in sensor pixels.
	X, Y, Diameter int
	// Angle is the streak's direction from the shadow's centre, in
	// degrees counterclockwise from the sensor's +x axis with y down, and
	// Radius its distance from it in sensor pixels.
	Angle  float64
	Radius int
	// Depth is how much darker the streak is than the sky on both sides of
	// it, less the typical dip of the shadow's other sectors, in percent
	// (negative: darker). Sigma is the spread of the other sectors' dips,
	// and Z how many of those Depth is.
	Depth float64
	Sigma float64
	Z     float64
	// Structure is how much the target's template varies in the streak's
	// sector (rimStructure), in percent of the sky.
	Structure float64
}

// RimStreak measures the rims of the shadows of a normalized sub map (as
// SubMap makes: 4×4 binned, sky 1, sensor orientation). Each shadow of at
// least RimMinDiameter and RimMinPeak is cut into RimSectors sectors; in
// each, a band rimBand wide is slid from rimInner to rimOuter of the
// radius, and its dip is how far its median falls below the lower of the
// medians just inside and just outside it, so the edge of a galaxy or a
// gradient across the band is not a dip. A sector's dip is its deepest.
// Stars and bright structure are masked first (MaskStars), and where the
// template is over RimBright or missing. A shadow whose
// rim lies on the target's own structure, more than RimMaxStructure in
// tmpl (the target's master in the sub's sensor frame, SensorTemplate), is
// passed over: a streak there can't be told from a dust lane. The streak
// is the deepest sector, over all shadows, that stands out RimMinZ from
// the median of its shadow's others (see better); ok is false when no
// shadow has enough measurable sectors.
func RimStreak(m, tmpl Map, shadows []Blob) (Rim, bool) {
	var best Rim
	found := false
	for _, s := range shadows {
		if s.Diameter < RimMinDiameter || s.Peak > RimMinPeak {
			continue
		}
		var st [RimSectors]float64
		if tmpl.W > 0 {
			var ok bool
			if st, ok = rimStructure(tmpl, s); !ok {
				continue
			}
		}
		r, ok := rimOf(m, tmpl, s, st)
		if !ok {
			continue
		}
		if !found || better(r, best) {
			best, found = r, true
		}
	}
	return best, found
}

// better is whether a is the streak to tell over b: the deeper of two that
// stand out, else the one that stands out more.
func better(a, b Rim) bool {
	sa, sb := a.Z >= RimMinZ, b.Z >= RimMinZ
	if sa && sb {
		return a.Depth < b.Depth
	}
	if sa != sb {
		return sa
	}
	return a.Z > b.Z
}

func rimOf(m, tmpl Map, s Blob, structure [RimSectors]float64) (Rim, bool) {
	x0, y0, x1, y1, cx, cy, R, ok := rimWindow(m, s)
	if !ok {
		return Rim{}, false
	}
	c := sub(m, x0, y0, x1, y1)
	MaskStars(c)
	if tmpl.W > 0 {
		for y := range c.H {
			for x := range c.W {
				if t := tmpl.Pix[(y0+y)*tmpl.W+x0+x]; isNaN(t) || t > RimBright {
					c.Pix[y*c.W+x] = float32(math.NaN())
				}
			}
		}
	}
	var pts []rimPx
	for y := range c.H {
		for x := range c.W {
			v := c.Pix[y*c.W+x]
			if isNaN(v) {
				continue
			}
			dx, dy := float64(x0+x)+0.5-cx, float64(y0+y)+0.5-cy
			r := math.Hypot(dx, dy)
			if r < rimInner*R-rimGap-rimRef || r > rimOuter*R+rimBand+rimGap+rimRef {
				continue
			}
			pts = append(pts, rimPx{r: r, sec: sector(dx, dy), v: v})
		}
	}
	dips, radii, valid := rimDips(pts, R)
	for k, st := range structure {
		if !(st <= RimMaxStructure) {
			valid[k] = false
		}
	}
	vals := make([]float64, 0, RimSectors)
	k, deepest, at, st := -1, 0.0, 0.0, 0.0
	for i, d := range dips {
		if !valid[i] {
			continue
		}
		vals = append(vals, d)
		if !onStreakSide(i) {
			continue
		}
		if k < 0 || d < deepest {
			k, deepest, at, st = i, d, radii[i], structure[i]
		}
	}
	if len(vals) < rimMinValid || k < 0 {
		return Rim{}, false
	}
	slices.Sort(vals)
	base := median64(vals)
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - base)
	}
	slices.Sort(dev)
	sigma := math.Max(1.4826*median64(dev), RimMinSigma)
	depth := deepest - base
	return Rim{
		X: s.X, Y: s.Y, Diameter: s.Diameter,
		Angle:  math.Mod(360-float64(k)*360/RimSectors, 360),
		Radius: int(math.Round(at * BinFactor)),
		Depth:  round3(depth), Sigma: round3(sigma), Z: math.Round(-depth/sigma*10) / 10, Structure: st,
	}, true
}

type rimPx struct {
	r   float64
	sec int
	v   float32
}

// rimDips is each sector's deepest dip, in percent, and the radius of its
// band's centre, in map pixels; valid is false for a sector without
// enough unmasked pixels.
func rimDips(pts []rimPx, radius float64) (dips, radii [RimSectors]float64, valid [RimSectors]bool) {
	var band, in, out []float32
	for k := range RimSectors {
		var sec []rimPx
		for _, p := range pts {
			if p.sec == k {
				sec = append(sec, p)
			}
		}
		for r0 := rimInner * radius; r0 < rimOuter*radius; r0++ {
			band, in, out = band[:0], in[:0], out[:0]
			for _, p := range sec {
				switch {
				case p.r >= r0 && p.r < r0+rimBand:
					band = append(band, p.v)
				case p.r >= r0-rimGap-rimRef && p.r < r0-rimGap:
					in = append(in, p.v)
				case p.r >= r0+rimBand+rimGap && p.r < r0+rimBand+rimGap+rimRef:
					out = append(out, p.v)
				}
			}
			if len(band) < rimMinBand || len(in) < rimMinRef || len(out) < rimMinRef {
				continue
			}
			ref := math.Min(float64(median(in)), float64(median(out)))
			if !(ref > 0) {
				continue
			}
			d := 100 * (float64(median(band))/ref - 1)
			if !valid[k] || d < dips[k] {
				dips[k], radii[k], valid[k] = d, r0+rimBand/2, true
			}
		}
	}
	return dips, radii, valid
}

// rimWindow is the map window around shadow s that its rim is measured in,
// its centre and radius in map pixels; ok is false when less than two
// thirds of it is on the map.
func rimWindow(m Map, s Blob) (x0, y0, x1, y1 int, cx, cy, radius float64, ok bool) {
	cx = (float64(s.X) + 0.5) / BinFactor
	cy = (float64(s.Y) + 0.5) / BinFactor
	radius = float64(s.Diameter) / 2 / BinFactor
	h := int(rimOuter*radius) + rimBand + rimGap + rimRef + 2
	x0, y0 = max(0, int(cx)-h), max(0, int(cy)-h)
	x1, y1 = min(m.W, int(cx)+h+1), min(m.H, int(cy)+h+1)
	return x0, y0, x1, y1, cx, cy, radius, x1-x0 >= 2*h/3 && y1-y0 >= 2*h/3
}

// rimStructure is how much the template varies around shadow s's rim in
// each sector, in percent of the sky: the spread (1.4826 MAD) of its 5×5
// median, stars and pixels over RimBright masked, less a plane fitted to
// the whole ring from 0.45 to 1.35 of the shadow's radius. The median
// keeps the master's own noise out, and a streak the master holds is too
// narrow to move the spread. A sector with too few pixels is NaN.
func rimStructure(tmpl Map, s Blob) ([RimSectors]float64, bool) {
	var out [RimSectors]float64
	x0, y0, x1, y1, cx, cy, radius, ok := rimWindow(tmpl, s)
	if !ok {
		return out, false
	}
	c := sub(tmpl, x0, y0, x1, y1)
	for i, v := range c.Pix {
		if v > RimBright {
			c.Pix[i] = float32(math.NaN())
		}
	}
	MaskStars(c)
	sm := MedianFilter(c, 5)
	var ata [3][3]float64
	var atb [3]float64
	type pt struct {
		x, y, v float64
		sec     int
	}
	var pts []pt
	for y := range c.H {
		for x := range c.W {
			v := sm.Pix[y*c.W+x]
			if isNaN(c.Pix[y*c.W+x]) || isNaN(v) {
				continue
			}
			px, py := float64(x0+x)+0.5-cx, float64(y0+y)+0.5-cy
			if r := math.Hypot(px, py); r < 0.45*radius || r > 1.35*radius {
				continue
			}
			pts = append(pts, pt{px / radius, py / radius, float64(v), sector(px, py)})
			t := [3]float64{1, px / radius, py / radius}
			for i := range 3 {
				for j := range 3 {
					ata[i][j] += t[i] * t[j]
				}
				atb[i] += t[i] * float64(v)
			}
		}
	}
	coef, ok := solve3(ata, atb)
	if !ok {
		return out, false
	}
	var res [RimSectors][]float64
	for _, p := range pts {
		res[p.sec] = append(res[p.sec], p.v-coef[0]-coef[1]*p.x-coef[2]*p.y)
	}
	for k, r := range res {
		if len(r) < rimMinStructure {
			out[k] = math.NaN()
			continue
		}
		slices.Sort(r)
		m := median64(r)
		for i := range r {
			r[i] = math.Abs(r[i] - m)
		}
		slices.Sort(r)
		out[k] = round3(100 * 1.4826 * median64(r))
	}
	return out, true
}

// sector is the rim sector of offset dx, dy from a shadow's centre.
func sector(dx, dy float64) int {
	a := math.Atan2(dy, dx) + math.Pi/RimSectors
	if a < 0 {
		a += 2 * math.Pi
	}
	return int(a/(2*math.Pi)*RimSectors) % RimSectors
}

func solve3(a [3][3]float64, b [3]float64) ([3]float64, bool) {
	var x [3]float64
	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) - a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) + a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])
	if math.Abs(det) < 1e-12 {
		return x, false
	}
	for k := range 3 {
		m := a
		for i := range 3 {
			m[i][k] = b[i]
		}
		x[k] = (m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])) / det
	}
	return x, true
}

// onStreakSide is whether sector k lies within RimSideSpread of the
// sensor's +x direction.
func onStreakSide(k int) bool {
	a := float64(k) * 360 / RimSectors
	return math.Min(a, 360-a) <= RimSideSpread
}
