package period

import (
	"cmp"
	"math"
	"slices"
	"sort"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	// minSrcPoints is how many points a light curve inside a merged series
	// needs, each with the largest light curve's points near it in time,
	// for its own zero point to be measured; smaller ones stay with the
	// largest.
	minSrcPoints = 10
	// srcNear is how many days from a point the largest light curve's
	// points may be to give the level it is measured against, and
	// srcNearMin how many of them it takes.
	srcNear    = 10.0
	srcNearMin = 3
	// srcSpan is how many days the compared points of a light curve must
	// span, two seasons' gap.
	srcSpan = 2 * seasonGap
	// minOutside is how many points outside the eclipses a light curve
	// needs, each with the largest light curve's points near it outside
	// the eclipses too, for its level to come from those points alone.
	minOutside = 5
)

// splitSrc splits time ordered points by their Src into one run per zero
// point, the run with the most points first and the rest by Src. Runs of
// fewer than minSrcPoints points join the first.
func splitSrc(ps []lc.Point) [][]lc.Point {
	by := map[int64][]lc.Point{}
	for _, p := range ps {
		by[p.Src] = append(by[p.Src], p)
	}
	if len(by) < 2 {
		return [][]lc.Point{ps}
	}
	srcs := make([]int64, 0, len(by))
	for s := range by {
		srcs = append(srcs, s)
	}
	slices.SortFunc(srcs, func(a, b int64) int {
		if c := cmp.Compare(len(by[b]), len(by[a])); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	out := [][]lc.Point{nil}
	small := false
	for i, s := range srcs {
		switch {
		case i == 0:
			out[0] = by[s]
		case len(by[s]) < minSrcPoints:
			out[0] = append(out[0], by[s]...)
			small = true
		default:
			out = append(out, by[s])
		}
	}
	if small {
		sortByTime(out[0])
	}
	return out
}

func sortByTime(ps []lc.Point) {
	slices.SortStableFunc(ps, func(a, b lc.Point) int { return cmp.Compare(a.BJD, b.BJD) })
}

// srcOffset is how much fainter a light curve, values y at times t in
// time order, sits than the largest one, values ly at times lt in time
// order: the median over its points of each value less the median of the
// largest one's values within srcNear days of it. Comparing points near
// in time keeps a slow change of the star out of the offset. ok is false
// when fewer than need points have srcNearMin such values near them, or
// when those points span under srcSpan days, as a light curve that only
// caught one episode of the star cannot tell its zero point from the
// episode.
func srcOffset(t, y, lt, ly []float64, need int) (float64, bool) {
	var d []float64
	first, last := math.Inf(1), math.Inf(-1)
	win := make([]float64, 0, 64)
	for i := range t {
		lo := sort.SearchFloat64s(lt, t[i]-srcNear)
		hi := sort.SearchFloat64s(lt, t[i]+srcNear)
		if hi-lo < srcNearMin {
			continue
		}
		win = append(win[:0], ly[lo:hi]...)
		d = append(d, y[i]-median(win))
		first, last = math.Min(first, t[i]), math.Max(last, t[i])
	}
	if len(d) < need || last-first < srcSpan {
		return 0, false
	}
	return median(d), true
}

// fieldRuns splits a series' time ordered points into one run per zero
// point as splitSrc does, and gives each run's offset from the first, by
// srcOffset on the magnitudes. A run too far in time from the first to be
// compared joins it, so a light curve that only caught the star during a
// change does not have the change taken out as an offset.
func fieldRuns(ps []lc.Point) ([][]lc.Point, []float64) {
	runs := splitSrc(ps)
	if len(runs) < 2 {
		return runs, []float64{0}
	}
	lt, ly := timesMags(runs[0], nil)
	out := [][]lc.Point{runs[0]}
	offs := []float64{0}
	merged := false
	for _, r := range runs[1:] {
		t, y := timesMags(r, nil)
		off, ok := srcOffset(t, y, lt, ly, minSrcPoints)
		if !ok {
			out[0] = append(out[0], r...)
			merged = true
			continue
		}
		out = append(out, r)
		offs = append(offs, off)
	}
	if merged {
		sortByTime(out[0])
	}
	return out, offs
}

// timesMags are the times and magnitudes of ps, less model when set.
func timesMags(ps []lc.Point, model func(lc.Point) float64) ([]float64, []float64) {
	t := make([]float64, len(ps))
	y := make([]float64, len(ps))
	for i, p := range ps {
		t[i], y[i] = p.BJD, p.Mag
		if model != nil {
			y[i] -= model(p)
		}
	}
	return t, y
}

// AlignSrc puts the light curves a series merges (lc.Point.Src: ZTF's one
// per field and CCD quadrant) on one zero point, its largest light
// curve's, and returns the series with the others' magnitudes moved. Each
// light curve is compared with the largest one at nearby times
// (srcOffset): with an eclipsing e on points outside the eclipses, or
// when too few of those, on all points less the model e predicts; with a
// smooth cycle on the points less the model; with no ephemeris on the
// magnitudes. A light curve that mostly catches eclipses so keeps its
// eclipses' depth. Series with one zero point, relative ones, light
// curves under ten points and those never near the largest in time are
// left as they are.
func AlignSrc(series []lc.Series, e *Ephemeris) []lc.Series {
	out := slices.Clone(series)
	for i, s := range out {
		if s.Relative || s.Survey == lc.Own {
			continue
		}
		runs := splitSrc(cleanPoints(s.Points))
		if len(runs) < 2 {
			continue
		}
		model := modelOf(e, BandKey(s.Survey, s.Band), dilution(s.Dilution))
		shift := map[int64]float64{}
		for _, r := range runs[1:] {
			if off, ok := alignOffset(r, runs[0], e, model); ok {
				shift[r[0].Src] = -off
			}
		}
		if len(shift) == 0 {
			continue
		}
		pts := slices.Clone(s.Points)
		for j := range pts {
			pts[j].Mag += shift[pts[j].Src]
		}
		out[i].Points = pts
	}
	return out
}

func alignOffset(r, lead []lc.Point, e *Ephemeris, model func(lc.Point) float64) (float64, bool) {
	if e != nil && e.Kind == KindEclipsing {
		outside := func(ps []lc.Point) []lc.Point {
			var o []lc.Point
			for _, p := range ps {
				if model(p) == 0 {
					o = append(o, p)
				}
			}
			return o
		}
		t, y := timesMags(outside(r), nil)
		lt, ly := timesMags(outside(lead), nil)
		if off, ok := srcOffset(t, y, lt, ly, minOutside); ok {
			return off, true
		}
	}
	t, y := timesMags(r, model)
	lt, ly := timesMags(lead, model)
	return srcOffset(t, y, lt, ly, minSrcPoints)
}

// modelOf is what e predicts a series sees at a point, with dil of its
// light the star's own; zero everywhere without a period.
func modelOf(e *Ephemeris, key string, dil float64) func(lc.Point) float64 {
	switch {
	case e == nil || e.Period <= 0:
		return func(lc.Point) float64 { return 0 }
	case e.Kind == KindSinusoidal:
		return func(p lc.Point) float64 { return e.Predict(p.BJD, key) * dil }
	}
	return func(p lc.Point) float64 { return dilute(e.Predict(p.BJD, key), dil) }
}

func dilution(d float64) float64 {
	if d <= 0 || d > 1 {
		return 1
	}
	return d
}

// densityWeightsJoint sets density weights over several series together,
// so a night two of them saw counts once.
func densityWeightsJoint(ss [][]obs) {
	counts := map[int64]int{}
	for _, o := range ss {
		for _, p := range o {
			counts[int64(math.Floor(p.t/densityBin))]++
		}
	}
	for _, o := range ss {
		for i := range o {
			o[i].w = 1 / float64(counts[int64(math.Floor(o[i].t/densityBin))])
		}
	}
}
