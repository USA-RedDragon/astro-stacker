// Package eclfacts measures what the survey light curves say about an
// eclipsing binary's dips at a fixed ephemeris: how many points and
// separate eclipses caught each dip, their depths with errors, whether
// the two dips differ, a limit on an unseen second dip, and the star's
// normal brightness.
package eclfacts

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

// point is one survey point put on its series' normal level: y is
// magnitudes fainter than normal.
type point struct {
	t, y, e float64
	key     string
	survey  string
}

// level is a series' normal magnitude: the median of its points outside
// every eclipse, or of all its points when none is outside.
func level(s lc.Series, e *period.Ephemeris) float64 {
	var out, all []float64
	for _, p := range s.Points {
		if !finite(p.Mag) {
			continue
		}
		all = append(all, p.Mag)
		if _, sh := inEclipse(e, p.BJD); sh == 0 {
			out = append(out, p.Mag)
		}
	}
	if len(out) >= 5 {
		return median(out)
	}
	return median(all)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := slices.Clone(v)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// inEclipse says which dip of e covers bjd and how deep in its shape, 0
// to 1; 0 outside every dip.
func inEclipse(e *period.Ephemeris, bjd float64) (int, float64) {
	if e == nil || e.Period <= 0 {
		return -1, 0
	}
	x := (bjd - e.T0) / e.Period
	for k, ec := range e.Eclipses {
		if sh := period.Shape(period.WrapPhase(x-ec.Phase), ec.Width, ec.Shape); sh > 0 {
			return k, sh
		}
	}
	return -1, 0
}

// cycleOf is the cycle number of the dip k minimum nearest bjd.
func cycleOf(e *period.Ephemeris, k int, bjd float64) int {
	return int(math.Round((bjd-e.T0)/e.Period - e.Eclipses[k].Phase))
}

// points puts every non-relative series on its own level; relative ones
// are already magnitudes fainter than normal.
func points(series []lc.Series, e *period.Ephemeris) []point {
	var out []point
	for _, s := range series {
		if s.Survey == lc.Own {
			continue
		}
		lvl := 0.0
		if !s.Relative {
			lvl = level(s, e)
		}
		if !finite(lvl) {
			continue
		}
		key := period.BandKey(s.Survey, s.Band)
		for _, p := range s.Points {
			if !finite(p.Mag) || !finite(p.BJD) {
				continue
			}
			err := p.Err
			if !(err > 0) || !finite(err) {
				err = 0.05
			}
			out = append(out, point{t: p.BJD, y: p.Mag - lvl, e: math.Max(err, 0.003), key: key, survey: s.Survey})
		}
	}
	return out
}

// Coverage is how the surveys caught one dip: Points is how many survey
// points, TESS aside (its dense sampling would swamp the count), fall
// inside it; Events how many separate eclipses of it any survey caught.
type Coverage struct {
	Points int `json:"points"`
	Events int `json:"events"`
}

func coverage(pts []point, e *period.Ephemeris) []Coverage {
	out := make([]Coverage, len(e.Eclipses))
	seen := make([]map[int]bool, len(e.Eclipses))
	for k := range seen {
		seen[k] = map[int]bool{}
	}
	for _, p := range pts {
		k, sh := inEclipse(e, p.t)
		if k < 0 || sh <= 0 {
			continue
		}
		if p.survey != lc.TESS {
			out[k].Points++
		}
		seen[k][cycleOf(e, k, p.t)] = true
	}
	for k := range out {
		out[k].Events = len(seen[k])
	}
	return out
}

// Depth is one dip's fitted depth in a band with its one sigma error.
type Depth struct {
	Value float64 `json:"value"`
	Err   float64 `json:"err"`
}

// bandFit is the dips' depths in one band, fitted together at fixed
// times and shapes, with the error of their difference.
type bandFit struct {
	key     string
	depths  []Depth
	diffErr float64
	core    []int
	chi2    float64
	n       int
}

// fitBands fits each band's dip depths by weighted least squares, the
// errors scaled by the scatter about the fit, after dropping points more
// than 5 sigma off.
func fitBands(pts []point, e *period.Ephemeris) map[string]*bandFit {
	byKey := map[string][]point{}
	for _, p := range pts {
		byKey[p.key] = append(byKey[p.key], p)
	}
	out := map[string]*bandFit{}
	for key, ps := range byKey {
		if f := fitDepths(ps, e); f != nil {
			f.key = key
			out[key] = f
		}
	}
	return out
}

func fitDepths(ps []point, e *period.Ephemeris) *bandFit {
	nd := len(e.Eclipses)
	if nd == 0 || nd > 2 {
		return nil
	}
	keep := make([]bool, len(ps))
	for i := range keep {
		keep[i] = true
	}
	var res *bandFit
	for range 3 {
		res = solve(ps, keep, e)
		if res == nil {
			return nil
		}
		s := math.Sqrt(math.Max(res.chi2/float64(max(res.n-nd, 1)), 1))
		changed := false
		for i, p := range ps {
			if !keep[i] {
				continue
			}
			if r := (p.y - model(e, res.depths, p.t)) / (p.e * s); math.Abs(r) > 5 {
				keep[i] = false
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return res
}

func model(e *period.Ephemeris, d []Depth, t float64) float64 {
	k, sh := inEclipse(e, t)
	if k < 0 || k >= len(d) {
		return 0
	}
	return d[k].Value * sh
}

// solve fits y = sum_k d_k shape_k(t) with the level held at the series
// medians.
func solve(ps []point, keep []bool, e *period.Ephemeris) *bandFit {
	nd := len(e.Eclipses)
	a := make([]float64, nd)
	b := make([]float64, nd)
	core := make([]int, nd)
	n := 0
	for i, p := range ps {
		if !keep[i] {
			continue
		}
		n++
		k, sh := inEclipse(e, p.t)
		if k < 0 {
			continue
		}
		w := 1 / (p.e * p.e)
		a[k] += w * sh * sh
		b[k] += w * sh * p.y
		if sh > 0.5 {
			core[k]++
		}
	}
	d := make([]Depth, nd)
	for k := range nd {
		if a[k] <= 0 {
			return nil
		}
		d[k].Value = b[k] / a[k]
	}
	var chi2 float64
	for i, p := range ps {
		if keep[i] {
			r := (p.y - model(e, d, p.t)) / p.e
			chi2 += r * r
		}
	}
	s := math.Sqrt(math.Max(chi2/float64(max(n-nd, 1)), 1))
	var de float64
	for k := range nd {
		d[k].Err = s / math.Sqrt(a[k])
		de += d[k].Err * d[k].Err
	}
	return &bandFit{depths: d, diffErr: math.Sqrt(de), core: core, chi2: chi2, n: n}
}

// Twin compares a star's two dips: DepthZ is the largest difference of
// their depths in any well sampled band, in sigma, with that band's
// depths; PhaseZ how far the second dip sits from mid-cycle, in sigma.
type Twin struct {
	Band     string  `json:"band"`
	Depth1   Depth   `json:"depth1"`
	Depth2   Depth   `json:"depth2"`
	DepthZ   float64 `json:"depth_z"`
	Phase2   float64 `json:"phase2"`
	PhaseErr float64 `json:"phase_err"`
	PhaseZ   float64 `json:"phase_z"`
}

// minCore is how many points each dip needs near its minimum for a band
// to count in the comparison.
const minCore = 3

// twin compares the two dips of e.
func twin(pts []point, e *period.Ephemeris) *Twin {
	if len(e.Eclipses) != 2 {
		return nil
	}
	fits := fitBands(pts, e)
	t := &Twin{Phase2: e.Eclipses[1].Phase}
	for _, key := range slices.Sorted(maps.Keys(fits)) {
		f := fits[key]
		if f.core[0] < minCore || f.core[1] < minCore || !(f.diffErr > 0) {
			continue
		}
		z := math.Abs(f.depths[0].Value-f.depths[1].Value) / f.diffErr
		if t.Band == "" || z > t.DepthZ {
			t.Band, t.Depth1, t.Depth2, t.DepthZ = key, f.depths[0], f.depths[1], z
		}
	}
	t.Phase2, t.PhaseErr = fitPhase2(pts, e)
	if t.PhaseErr > 0 {
		t.PhaseZ = math.Abs(t.Phase2-0.5) / t.PhaseErr
	}
	return t
}

// fitPhase2 finds the second dip's phase by the least total chi-square of
// the per band depth fits over a grid of phases, and its one sigma error
// where that chi-square, scaled by its reduced value, rises by one.
func fitPhase2(pts []point, e *period.Ephemeris) (float64, float64) {
	w := e.Eclipses[1].Width
	ph0 := e.Eclipses[1].Phase
	chi := func(ph float64) (float64, int) {
		c := *e
		c.Eclipses = slices.Clone(e.Eclipses)
		c.Eclipses[1].Phase = ph
		var sum float64
		n := 0
		for _, f := range fitBands(pts, &c) {
			sum += f.chi2
			n += f.n
		}
		return sum, n
	}
	const steps = 80
	best, bestChi, bestN := ph0, math.Inf(1), 0
	for i := 0; i <= steps; i++ {
		ph := ph0 + w*(float64(i)/steps*2-1)
		if c, n := chi(ph); c < bestChi {
			best, bestChi, bestN = ph, c, n
		}
	}
	if math.IsInf(bestChi, 1) || bestN == 0 {
		return ph0, 0
	}
	scale := math.Max(bestChi/float64(bestN), 1)
	fine := w / steps / 4
	for range 2 {
		for _, ph := range []float64{best - fine, best + fine} {
			if c, _ := chi(ph); c < bestChi {
				best, bestChi = ph, c
			}
		}
	}
	var lo, hi float64
	for _, side := range []float64{-1, 1} {
		step := w / steps / 2
		ph := best
		for range 4 * steps {
			ph += side * step
			if c, _ := chi(ph); (c-bestChi)/scale >= 1 {
				break
			}
		}
		if side < 0 {
			lo = ph
		} else {
			hi = ph
		}
	}
	return best, (hi - lo) / 2
}

// doubled is e at twice its period with its one dip on both halves, for
// a star that shows one dip per cycle.
func doubled(e *period.Ephemeris) *period.Ephemeris {
	if len(e.Eclipses) != 1 {
		return nil
	}
	d := e.Eclipses[0]
	return &period.Ephemeris{Period: 2 * e.Period, PeriodErr: 2 * e.PeriodErr, T0: e.T0, T0Err: e.T0Err, Kind: e.Kind,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: d.Width / 2, Shape: d.Shape, Depth: d.Depth},
			{Phase: 0.5, Width: d.Width / 2, Shape: d.Shape, Depth: d.Depth},
		}}
}

// secondaryLimit is the three sigma upper limit, in key's band, on a dip
// at mid-cycle as wide and shaped as the one seen, for a star with one dip
// per cycle. ok is false without points there.
func secondaryLimit(pts []point, e *period.Ephemeris, key string) (float64, bool) {
	if len(e.Eclipses) != 1 {
		return 0, false
	}
	c := *e
	c.Eclipses = []period.Eclipse{e.Eclipses[0], {Phase: 0.5, Width: e.Eclipses[0].Width, Shape: e.Eclipses[0].Shape}}
	var ps []point
	for _, p := range pts {
		if p.key == key {
			ps = append(ps, p)
		}
	}
	f := fitDepths(ps, &c)
	if f == nil || f.core[1] < minCore {
		return 0, false
	}
	return math.Max(f.depths[1].Value, 0) + 3*f.depths[1].Err, true
}

// Normal is the star's brightness outside the eclipses in one band:
// the median of that band's points outside every dip.
type Normal struct {
	Survey string  `json:"survey"`
	Band   string  `json:"band"`
	Mag    float64 `json:"mag"`
	Points int     `json:"points"`
}

// minNormal is how many points outside the eclipses a band needs to give
// the star's normal brightness.
const minNormal = 20

// normal is the median ZTF r magnitude outside the eclipses, or ZTF g
// when r has too few points there.
func normal(series []lc.Series, e *period.Ephemeris) *Normal {
	for _, band := range []string{"r", "g"} {
		var out []float64
		for _, s := range series {
			if s.Survey != lc.ZTF || s.Band != band || s.Relative {
				continue
			}
			for _, p := range s.Points {
				if !finite(p.Mag) {
					continue
				}
				if _, sh := inEclipse(e, p.BJD); sh == 0 {
					out = append(out, p.Mag)
				}
			}
		}
		if len(out) >= minNormal {
			return &Normal{Survey: lc.ZTF, Band: band, Mag: median(out), Points: len(out)}
		}
	}
	return nil
}

// fadeWidth is the share of a dip's fitted half width where the dip is
// deeper than durationLevel of its full depth.
const durationLevel = 0.05

func fadeWidth(shape float64) float64 {
	if !(shape > 0) {
		return 1
	}
	return math.Sqrt(math.Max(1-math.Pow(durationLevel, 1/shape), 0))
}

// Duration is how long dip k lasts in days, from where the fitted dip
// first reaches durationLevel of its depth to where it last does.
func Duration(e *period.Ephemeris, k int) float64 {
	if e == nil || k < 0 || k >= len(e.Eclipses) {
		return 0
	}
	d := e.Eclipses[k]
	return 2 * d.Width * fadeWidth(d.Shape) * e.Period
}

// Summary is what the surveys say about a star's dips at one ephemeris.
type Summary struct {
	Period    float64    `json:"period"`
	Coverage  []Coverage `json:"coverage"`
	Twin      *Twin      `json:"twin,omitempty"`
	Secondary *Limit     `json:"secondary,omitempty"`
	Normal    *Normal    `json:"normal,omitempty"`
}

// Limit is an upper limit on a dip's depth in a band.
type Limit struct {
	Band string  `json:"band"`
	Mag  float64 `json:"mag"`
}

// Summarize measures series at e. A star with one dip per cycle gets its
// limit on a second dip at mid-cycle.
func Summarize(series []lc.Series, e *period.Ephemeris) *Summary {
	if e == nil || e.Period <= 0 || len(e.Eclipses) == 0 || len(e.Eclipses) > 2 {
		return nil
	}
	pts := points(series, e)
	if len(pts) == 0 {
		return nil
	}
	s := &Summary{Period: e.Period, Coverage: coverage(pts, e), Twin: twin(pts, e), Normal: normal(series, e)}
	if len(e.Eclipses) == 1 {
		for _, key := range []string{"ZTF r", "ZTF g"} {
			if v, ok := secondaryLimit(pts, e, key); ok {
				s.Secondary = &Limit{Band: strings.TrimPrefix(key, "ZTF "), Mag: v}
				break
			}
		}
	}
	return s
}

// Doubled is e at twice its period with its one dip on both halves, for a
// star that shows one dip per cycle; nil for any other.
func Doubled(e *period.Ephemeris) *period.Ephemeris { return doubled(e) }

// InEclipse says which dip of e covers bjd and how deep in its shape, 0
// to 1; dip -1 outside every dip.
func InEclipse(e *period.Ephemeris, bjd float64) (int, float64) { return inEclipse(e, bjd) }
