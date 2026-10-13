package horizonsurvey

import (
	"math"
	"slices"
)

const (
	nightLow        = 0.6
	nightHigh       = 1.7
	steepPerWidth   = 1.5
	starPeakSigma   = 5.0
	minStarPixels   = 3
	maxStarFrac     = 0.0008
	minStarsForRef  = 3
	starlessExpect  = 2.0
	starDensityHold = 0.5
	minObstFrac     = 0.01
	minSkyFrac      = 0.02
	starFloor       = 0.4
	minSkySNR       = 3.0
	skyLikeTol      = 0.15
)

const (
	RefStars     = "stars"
	RefReference = "reference"
	RefFrame     = "frame"
)

type Star struct {
	X, Y   int
	Pixels int
}

type nightFrame struct {
	pix     []float32
	w, h, b int
	bw, bh  int
	med     []float64
	stars   []Star
	starIn  []int
	starBlk []bool
	wide    []float64
}

func (f *nightFrame) starLevel() float64 {
	lv := make([]float64, 0, len(f.stars))
	for _, s := range f.stars {
		lv = append(lv, f.bg(s.X, s.Y))
	}
	return percentile(lv, 0.5)
}

func (f *nightFrame) keepStarsNear(level float64) {
	kept := f.stars[:0]
	for _, s := range f.stars {
		if v := f.bg(s.X, s.Y); v >= nightLow*level && v <= nightHigh*level {
			kept = append(kept, s)
			continue
		}
		f.starIn[min(s.Y/f.b, f.bh-1)*f.bw+min(s.X/f.b, f.bw-1)]--
	}
	f.stars = kept
}

func boxSmooth(pix []float32, w, h int) []float32 {
	rows := make([]float32, len(pix))
	for y := range h {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			rows[i] = pix[i-1] + pix[i] + pix[i+1]
		}
	}
	out := make([]float32, len(pix))
	for y := 1; y+1 < h; y++ {
		for x := 1; x+1 < w; x++ {
			i := y*w + x
			out[i] = (rows[i-w] + rows[i] + rows[i+w]) / 9
		}
	}
	return out
}

func (f *nightFrame) bg(x, y int) float64 {
	bx, by := min(x/f.b, f.bw-1), min(y/f.b, f.bh-1)
	return f.med[by*f.bw+bx]
}

func (f *nightFrame) findStars(skyRef float64) {
	f.wide = medianFilterR(f.med, nil, f.bw, f.bh, 3)
	s := boxSmooth(f.pix, f.w, f.h)
	offset, spread := f.residualStats(s)
	claimed := make([]bool, len(s))
	f.starIn = make([]int, f.bw*f.bh)
	f.starBlk = make([]bool, f.bw*f.bh)
	maxStar := max(30, int(maxStarFrac*float64(f.w*f.h)))
	queue := make([]int, 0, maxStar+16)
	w := f.w
	for y := 3; y+3 < f.h; y++ {
		for x := 3; x+3 < w; x++ {
			i := y*w + x
			v := s[i]
			if claimed[i] {
				continue
			}
			bi := min(y/f.b, f.bh-1)*f.bw + min(x/f.b, f.bw-1)
			bg := f.wide[bi]
			if bg < starFloor*skyRef || bg < minSkySNR*spread[bi] || float64(v)-bg <= offset[bi]+starPeakSigma*spread[bi] {
				continue
			}
			if v < s[i-1] || v < s[i+1] || v < s[i-w] || v < s[i+w] || v < s[i-w-1] || v < s[i-w+1] || v < s[i+w-1] || v < s[i+w+1] {
				continue
			}
			base := bg + offset[bi]
			half := float32(base + (float64(v)-base)/2)
			queue = append(queue[:0], i)
			claimed[i] = true
			n := 0
			for k := 0; k < len(queue); k++ {
				j := queue[k]
				n++
				if n > maxStar {
					break
				}
				jx, jy := j%w, j/w
				for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := jx+d[0], jy+d[1]
					if nx < 1 || ny < 1 || nx+1 >= w || ny+1 >= f.h {
						continue
					}
					ni := ny*w + nx
					if !claimed[ni] && s[ni] > half {
						claimed[ni] = true
						queue = append(queue, ni)
					}
				}
			}
			if n > maxStar {
				f.claimLight(s, claimed, queue, half)
				continue
			}
			if f.rawFootprint(x, y, base) < minStarPixels {
				continue
			}
			for _, j := range queue {
				bx, by := min(j%w/f.b, f.bw-1), min(j/w/f.b, f.bh-1)
				for yy := max(0, by-1); yy <= min(f.bh-1, by+1); yy++ {
					for xx := max(0, bx-1); xx <= min(f.bw-1, bx+1); xx++ {
						f.starBlk[yy*f.bw+xx] = true
					}
				}
			}
			f.stars = append(f.stars, Star{X: x, Y: y, Pixels: n})
			f.starIn[min(y/f.b, f.bh-1)*f.bw+min(x/f.b, f.bw-1)]++
		}
	}
}

func (f *nightFrame) residualStats(s []float32) (offset, spread []float64) {
	n := f.bw * f.bh
	offset, spread = make([]float64, n), make([]float64, n)
	buf := make([]float64, 0, f.b*f.b)
	for by := range f.bh {
		for bx := range f.bw {
			buf = buf[:0]
			bg := f.wide[by*f.bw+bx]
			for y := max(1, by*f.b); y < min(f.h-1, (by+1)*f.b); y++ {
				for x := max(1, bx*f.b); x < min(f.w-1, (bx+1)*f.b); x++ {
					buf = append(buf, float64(s[y*f.w+x])-bg)
				}
			}
			if len(buf) == 0 {
				continue
			}
			slices.Sort(buf)
			c := buf[len(buf)/2]
			for i := range buf {
				buf[i] = math.Abs(buf[i] - c)
			}
			slices.Sort(buf)
			offset[by*f.bw+bx], spread[by*f.bw+bx] = c, buf[len(buf)/2]*1.4826
		}
	}
	offset, spread = medianFilterR(offset, nil, f.bw, f.bh, 3), medianFilterR(spread, nil, f.bw, f.bh, 3)
	floor := percentile(spread, 0.5)
	for i := range spread {
		spread[i] = math.Max(spread[i], floor)
	}
	return offset, spread
}

func (f *nightFrame) claimLight(s []float32, claimed []bool, queue []int, half float32) {
	w := f.w
	for k := 0; k < len(queue); k++ {
		j := queue[k]
		jx, jy := j%w, j/w
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := jx+d[0], jy+d[1]
			if nx < 1 || ny < 1 || nx+1 >= w || ny+1 >= f.h {
				continue
			}
			ni := ny*w + nx
			if !claimed[ni] && s[ni] > half {
				claimed[ni] = true
				queue = append(queue, ni)
			}
		}
	}
}

func (f *nightFrame) rawFootprint(x, y int, bg float64) int {
	peak := float32(0)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			peak = max(peak, f.pix[(y+dy)*f.w+x+dx])
		}
	}
	half := float32(bg + (float64(peak)-bg)/2)
	n := 0
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if f.pix[(y+dy)*f.w+x+dx] > half {
				n++
			}
		}
	}
	return n
}

func (f *nightFrame) steep(level float64) []bool {
	d := max(2, f.bw/60)
	bw, bh := f.bw, f.bh
	smooth := medianFilterR(f.med, f.starBlk, bw, bh, 2)
	sum := make([]float64, (bw+1)*(bh+1))
	for y := range bh {
		for x := range bw {
			sum[(y+1)*(bw+1)+x+1] = smooth[y*bw+x] + sum[y*(bw+1)+x+1] + sum[(y+1)*(bw+1)+x] - sum[y*(bw+1)+x]
		}
	}
	mean := func(x0, y0, x1, y1 int) (float64, bool) {
		x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, bw), min(y1, bh)
		if x1 <= x0 || y1 <= y0 {
			return 0, false
		}
		s := sum[y1*(bw+1)+x1] - sum[y0*(bw+1)+x1] - sum[y1*(bw+1)+x0] + sum[y0*(bw+1)+x0]
		return s / float64((x1-x0)*(y1-y0)), true
	}
	limit := steepPerWidth * level / float64(bw)
	out := make([]bool, len(f.med))
	for by := range bh {
		for bx := range bw {
			g := 0.0
			if l, ok := mean(bx-d, by-d, bx, by+d+1); ok {
				if r, ok := mean(bx+1, by-d, bx+d+1, by+d+1); ok {
					g = math.Max(g, math.Abs(r-l)/float64(d+1))
				}
			}
			if l, ok := mean(bx-d, by-d, bx+d+1, by); ok {
				if r, ok := mean(bx-d, by+1, bx+d+1, by+d+1); ok {
					g = math.Max(g, math.Abs(r-l)/float64(d+1))
				}
			}
			out[by*bw+bx] = g > limit
		}
	}
	r := d + 2
	for by := range bh {
		for bx := range bw {
			i := by*bw + bx
			if !out[i] {
				continue
			}
			lo, hi := math.Inf(1), math.Inf(-1)
			for y := max(0, by-r); y <= min(bh-1, by+r); y++ {
				for x := max(0, bx-r); x <= min(bw-1, bx+r); x++ {
					lo, hi = math.Min(lo, smooth[y*bw+x]), math.Max(hi, smooth[y*bw+x])
				}
			}
			if hi <= nightHigh*level && smooth[i] >= (lo+hi)/2 {
				out[i] = false
			}
		}
	}
	return out
}

func medianFilterR(v []float64, skip []bool, bw, bh, radius int) []float64 {
	out := make([]float64, len(v))
	buf := make([]float64, 0, (2*radius+3)*(2*radius+3))
	for by := range bh {
		for bx := range bw {
			buf = buf[:0]
			for r := radius; r <= radius+1 && len(buf) == 0; r++ {
				for y := max(0, by-r); y <= min(bh-1, by+r); y++ {
					for x := max(0, bx-r); x <= min(bw-1, bx+r); x++ {
						if skip == nil || !skip[y*bw+x] {
							buf = append(buf, v[y*bw+x])
						}
					}
				}
			}
			if len(buf) == 0 {
				out[by*bw+bx] = v[by*bw+bx]
				continue
			}
			slices.Sort(buf)
			out[by*bw+bx] = buf[len(buf)/2]
		}
	}
	return out
}

func components(mask []bool, bw, bh int) [][]int {
	label := make([]int, len(mask))
	for i := range label {
		label[i] = -1
	}
	var comps [][]int
	for i, m := range mask {
		if !m || label[i] >= 0 {
			continue
		}
		id := len(comps)
		comp := []int{i}
		label[i] = id
		for k := 0; k < len(comp); k++ {
			j := comp[k]
			x, y := j%bw, j/bw
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx < 0 || ny < 0 || nx >= bw || ny >= bh {
					continue
				}
				ni := ny*bw + nx
				if mask[ni] && label[ni] < 0 {
					label[ni] = id
					comp = append(comp, ni)
				}
			}
		}
		comps = append(comps, comp)
	}
	return comps
}

func skyLike(comp []int, med []float64, level float64) bool {
	v := make([]float64, len(comp))
	for k, i := range comp {
		v[k] = med[i]
	}
	slices.Sort(v)
	lo, mid, hi := v[len(v)/10], v[len(v)/2], v[len(v)*9/10]
	return math.Abs(mid-level) <= skyLikeTol*level && hi-lo <= skyLikeTol*level
}

func darkShare(comp []int, med []float64, level float64) float64 {
	n := 0
	for _, i := range comp {
		if med[i] < nightLow*level {
			n++
		}
	}
	return float64(n) / float64(len(comp))
}

func touchesBorder(comp []int, bw, bh int) bool {
	for _, i := range comp {
		x, y := i%bw, i/bw
		if x == 0 || y == 0 || x == bw-1 || y == bh-1 {
			return true
		}
	}
	return false
}

func (f *nightFrame) reference(skyRef float64) (float64, string) {
	if len(f.stars) >= minStarsForRef {
		f.keepStarsNear(f.starLevel())
	}
	switch {
	case len(f.stars) >= minStarsForRef:
		return f.starLevel(), RefStars
	case skyRef > 0:
		return skyRef, RefReference
	default:
		return percentile(f.med, 0.5), RefFrame
	}
}

func (f *nightFrame) starsIn(comp []int) int {
	n := 0
	for _, i := range comp {
		n += f.starIn[i]
	}
	return n
}

func (f *nightFrame) density(comps [][]int, ref float64) float64 {
	if len(f.stars) < minStarsForRef {
		return starDensityHold * ref
	}
	area := 0
	for _, c := range comps {
		if f.starsIn(c) > 0 {
			area += len(c)
		}
	}
	if area == 0 {
		return starDensityHold * ref
	}
	return float64(len(f.stars)) / float64(area)
}

func (f *nightFrame) skyMask(level, ref float64) ([]bool, float64) {
	steep := f.steep(level)
	cand := make([]bool, len(f.med))
	for i, v := range f.med {
		cand[i] = f.starBlk[i] || (v >= nightLow*level && v <= nightHigh*level && !steep[i])
	}
	comps := components(cand, f.bw, f.bh)
	density := f.density(comps, ref)
	sky := make([]bool, len(f.med))
	for _, c := range comps {
		if f.starsIn(c) == 0 && density*float64(len(c)) >= starlessExpect && !skyLike(c, f.med, level) {
			continue
		}
		for _, i := range c {
			sky[i] = true
		}
	}
	return majority(majority(sky, f.bw, f.bh), f.bw, f.bh), density
}

func (f *nightFrame) cleanup(sky []bool, level float64) {
	minSky, minObst := minSkyFrac*float64(len(sky)), minObstFrac*float64(len(sky))
	for _, c := range components(sky, f.bw, f.bh) {
		if touchesBorder(c, f.bw, f.bh) && (f.starsIn(c) > 0 || float64(len(c)) >= minSky) {
			continue
		}
		for _, i := range c {
			sky[i] = false
		}
	}
	obst := make([]bool, len(sky))
	for i, s := range sky {
		obst[i] = !s
	}
	for _, c := range components(obst, f.bw, f.bh) {
		if touchesBorder(c, f.bw, f.bh) && (float64(len(c)) >= minObst || darkShare(c, f.med, level) >= 0.5) {
			continue
		}
		for _, i := range c {
			sky[i] = true
		}
	}
}

func segmentNight(pix []float32, w, h int, o SegmentOptions) Segmentation {
	b := BlockSize(w)
	med, _, bw, bh := blockStats(pix, w, h, b)
	f := &nightFrame{pix: pix, w: w, h: h, b: b, bw: bw, bh: bh, med: med}
	f.findStars(o.SkyRef)
	level, basis := f.reference(o.SkyRef)
	sky, density := f.skyMask(level, o.StarDensity)
	f.cleanup(sky, level)
	if basis == RefFrame {
		clear(sky)
	}
	seg := Segmentation{
		Mask: &Mask{BW: bw, BH: bh, Block: b, Sky: sky, Level: med}, Stars: len(f.stars), StarList: f.stars, StarDensity: density,
		StarRefUsed: o.StarDensity, RefBasis: basis, Peak: percentile(med, 0.99), Median: percentile(med, 0.5),
	}
	seg.Saturated = seg.Peak >= saturated
	seg.finish(med)
	return seg
}

func (seg *Segmentation) finish(med []float64) {
	obst := 0
	var skyVals []float64
	for i, s := range seg.Mask.Sky {
		if s {
			skyVals = append(skyVals, med[i])
		} else {
			obst++
		}
	}
	seg.ObstFrac = float64(obst) / float64(len(seg.Mask.Sky))
	if len(skyVals) > 0 {
		slices.Sort(skyVals)
		seg.SkyLevel = skyVals[len(skyVals)/2]
	}
	switch {
	case seg.ObstFrac <= edgeFraction:
		seg.Class = ClassSky
	case seg.ObstFrac >= 1-edgeFraction:
		seg.Class = ClassObst
	default:
		seg.Class = ClassEdge
	}
}
