package stacking

import (
	"math"
	"slices"
)

const (
	starSigma       = 10.0
	starPeakFrac    = 0.9
	starPeakRadius  = 3
	seamAperture    = 4.0
	starRingInner   = 6.0
	starRingOuter   = 9.0
	starEdge        = 10
	maxSeamStars    = 6000
	starMatchRadius = 3.0
	minSeamStars    = 5
	noiseTileBlocks = 8
	minTileBlocks   = noiseTileBlocks * noiseTileBlocks / 2
	skySampleTarget = 200_000
)

type seamStar struct {
	X, Y float64
	Flux float64
}

type starMatch struct {
	A, B   seamStar
	Offset float64
}

type mosaicNoise struct {
	Median   float64
	P90      float64
	Max      float64
	MaxPanel int
	Tiles    int
	Scales   []float64
}

func skyStats(data []float32, sat float32) (bg, sigma float64) {
	stride := max(1, len(data)/skySampleTarget)
	var s []float64
	for i := 0; i < len(data); i += stride {
		if v := data[i]; v != 0 && v < sat {
			s = append(s, float64(v))
		}
	}
	if len(s) < 100 {
		return 0, 0
	}
	slices.Sort(s)
	bg = s[len(s)/2]
	dev := make([]float64, len(s))
	for i, v := range s {
		dev[i] = math.Abs(v - bg)
	}
	slices.Sort(dev)
	return bg, madToSigma * dev[len(dev)/2]
}

func detectStars(data []float32, w, h int, at placement, sat float32) []seamStar {
	bg, sigma := skyStats(data, sat)
	if !(sigma > 0) {
		return nil
	}
	thr := float32(bg + starSigma*sigma)
	peakMax := sat * starPeakFrac
	var out []seamStar
	ring := make([]float64, 0, 256)
	for y := starEdge; y < h-starEdge; y++ {
		for x := starEdge; x < w-starEdge; x++ {
			v := data[y*w+x]
			if v < thr || v >= peakMax || !localPeak(data, w, x, y, v) {
				continue
			}
			s, ok := measureStar(data, w, x, y, sat, ring[:0])
			if !ok {
				continue
			}
			s.X += float64(at.X)
			s.Y += float64(at.Y)
			out = append(out, s)
		}
	}
	if len(out) > maxSeamStars {
		slices.SortFunc(out, func(a, b seamStar) int {
			switch {
			case a.Flux > b.Flux:
				return -1
			case a.Flux < b.Flux:
				return 1
			}
			return 0
		})
		out = out[:maxSeamStars]
	}
	return out
}

func localPeak(data []float32, w, x, y int, v float32) bool {
	for dy := -starPeakRadius; dy <= starPeakRadius; dy++ {
		for dx := -starPeakRadius; dx <= starPeakRadius; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			u := data[(y+dy)*w+x+dx]
			if u > v || (u == v && (dy < 0 || (dy == 0 && dx < 0))) {
				return false
			}
		}
	}
	return true
}

func measureStar(data []float32, w, x, y int, sat float32, ring []float64) (seamStar, bool) {
	r := int(starRingOuter)
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			u := data[(y+dy)*w+x+dx]
			if u == 0 || u >= sat {
				return seamStar{}, false
			}
			if d := math.Hypot(float64(dx), float64(dy)); d >= starRingInner && d <= starRingOuter {
				ring = append(ring, float64(u))
			}
		}
	}
	slices.Sort(ring)
	lbg := ring[len(ring)/2]
	var sx, sy, sw, flux float64
	a := int(seamAperture)
	for dy := -a; dy <= a; dy++ {
		for dx := -a; dx <= a; dx++ {
			if math.Hypot(float64(dx), float64(dy)) > seamAperture {
				continue
			}
			u := float64(data[(y+dy)*w+x+dx]) - lbg
			flux += u
			if u > 0 {
				sx, sy, sw = sx+u*float64(dx), sy+u*float64(dy), sw+u
			}
		}
	}
	if !(flux > 0) || !(sw > 0) {
		return seamStar{}, false
	}
	return seamStar{X: float64(x) + 0.5 + sx/sw, Y: float64(y) + 0.5 + sy/sw, Flux: flux}, true
}

func inOverlap(a, b binnedPanel, s seamStar) bool {
	bx, by := int(s.X/matchBin), int(s.Y/matchBin)
	if bx < 0 || by < 0 || bx >= min(a.W, b.W) || by >= min(a.H, b.H) {
		return false
	}
	return !math.IsNaN(float64(a.Data[by*a.W+bx])) && !math.IsNaN(float64(b.Data[by*b.W+bx]))
}

type starGrid map[[2]int][]int

func gridOf(stars []seamStar) starGrid {
	g := starGrid{}
	for i, s := range stars {
		k := [2]int{int(math.Floor(s.X / starMatchRadius)), int(math.Floor(s.Y / starMatchRadius))}
		g[k] = append(g[k], i)
	}
	return g
}

func (g starGrid) nearest(stars []seamStar, p seamStar) (int, float64) {
	cx, cy := int(math.Floor(p.X/starMatchRadius)), int(math.Floor(p.Y/starMatchRadius))
	best, bestD := -1, math.Inf(1)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			for _, i := range g[[2]int{cx + dx, cy + dy}] {
				if d := math.Hypot(stars[i].X-p.X, stars[i].Y-p.Y); d < bestD {
					best, bestD = i, d
				}
			}
		}
	}
	if bestD > starMatchRadius {
		return -1, 0
	}
	return best, bestD
}

func matchSeamStars(a, b binnedPanel, sa, sb []seamStar) []starMatch {
	var oa, ob []seamStar
	for _, s := range sa {
		if inOverlap(a, b, s) {
			oa = append(oa, s)
		}
	}
	for _, s := range sb {
		if inOverlap(a, b, s) {
			ob = append(ob, s)
		}
	}
	if len(oa) == 0 || len(ob) == 0 {
		return nil
	}
	ga, gb := gridOf(oa), gridOf(ob)
	var out []starMatch
	for i, s := range oa {
		j, d := gb.nearest(ob, s)
		if j < 0 {
			continue
		}
		if back, _ := ga.nearest(oa, ob[j]); back != i {
			continue
		}
		out = append(out, starMatch{A: s, B: ob[j], Offset: d})
	}
	return out
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	slices.Sort(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(len(s)-1, max(0, i))]
}

func starStats(m []starMatch) (n int, offMedian, offP90, ratio float64) {
	if len(m) < minSeamStars {
		return len(m), math.NaN(), math.NaN(), math.NaN()
	}
	offs := make([]float64, len(m))
	logs := make([]float64, len(m))
	for i, s := range m {
		offs[i] = s.Offset
		logs[i] = math.Log(s.A.Flux / s.B.Flux)
	}
	return len(m), percentile(offs, 0.5), percentile(offs, 0.9), math.Exp(percentile(logs, 0.5))
}

func panelScales(n int, seams []seamMeasure) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	var total float64
	for _, s := range seams {
		if s.StarMatches >= minSeamStars && s.FluxRatio > 0 {
			total += float64(s.StarMatches)
		}
	}
	if n == 0 || total == 0 {
		return out
	}
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n+1)
		m[i][i] = 1e-9 * total
	}
	for _, s := range seams {
		if s.StarMatches < minSeamStars || !(s.FluxRatio > 0) {
			continue
		}
		w, d := float64(s.StarMatches), math.Log(s.FluxRatio)
		m[s.I][s.I] += w
		m[s.J][s.J] += w
		m[s.I][s.J] -= w
		m[s.J][s.I] -= w
		m[s.I][n] += w * d
		m[s.J][n] -= w * d
	}
	x, ok := solveLinear(m)
	if !ok {
		return out
	}
	for i := range out {
		out[i] = math.Exp(x[i])
	}
	return out
}

func measureMosaicNoise(binned []binnedPanel, scales []float64) mosaicNoise {
	res := mosaicNoise{MaxPanel: -1, Scales: scales, Median: math.NaN(), P90: math.NaN(), Max: math.NaN()}
	if len(binned) == 0 {
		return res
	}
	tw, th := 0, 0
	for _, b := range binned {
		tw, th = max(tw, (b.W+noiseTileBlocks-1)/noiseTileBlocks), max(th, (b.H+noiseTileBlocks-1)/noiseTileBlocks)
	}
	type tile struct {
		sigma float64
		worst int
	}
	var tiles []tile
	vals := make([]float64, 0, noiseTileBlocks*noiseTileBlocks)
	for ty := range th {
		for tx := range tw {
			var inv, worstSigma float64
			worst := -1
			for k, b := range binned {
				vals = vals[:0]
				for by := ty * noiseTileBlocks; by < min(b.H, (ty+1)*noiseTileBlocks); by++ {
					for bx := tx * noiseTileBlocks; bx < min(b.W, (tx+1)*noiseTileBlocks); bx++ {
						if v := b.Noise[by*b.W+bx]; !math.IsNaN(float64(v)) && v > 0 {
							vals = append(vals, float64(v))
						}
					}
				}
				if len(vals) < minTileBlocks {
					continue
				}
				s := percentile(vals, 0.5) / scales[k]
				inv += 1 / (s * s)
				if s > worstSigma {
					worstSigma, worst = s, k
				}
			}
			if inv > 0 {
				tiles = append(tiles, tile{sigma: 1 / math.Sqrt(inv), worst: worst})
			}
		}
	}
	if len(tiles) == 0 {
		return res
	}
	sig := make([]float64, len(tiles))
	maxAt := 0
	for i, t := range tiles {
		sig[i] = t.sigma
		if t.sigma > tiles[maxAt].sigma {
			maxAt = i
		}
	}
	res.Tiles = len(tiles)
	res.Median, res.P90, res.Max = percentile(sig, 0.5), percentile(sig, 0.9), tiles[maxAt].sigma
	res.MaxPanel = tiles[maxAt].worst
	return res
}
