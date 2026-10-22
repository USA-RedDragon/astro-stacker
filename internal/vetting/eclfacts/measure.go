// Package eclfacts measures what the survey light curves say about an
// eclipsing binary's dips at a fixed ephemeris: how many points and
// separate eclipses caught each dip, their depths with errors, whether
// the two dips differ, a limit on an unseen second dip, and the star's
// normal brightness.
package eclfacts

import (
	"cmp"
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

// Twin compares a star's two dips: DepthZ is the difference of their
// depths in a well sampled band where both dips are fainter than normal,
// in sigma, with that band's depths: the only band's when one band can
// tell, else the second largest among bands where the same dip is the
// deeper, so one band's systematics cannot split the dips alone. When
// bands disagree on which dip is deeper, or only one of several finds
// them apart, it is the band that measures the difference best. Band is
// empty when no band can tell. PhaseZ is how far
// the second dip sits from mid-cycle, in sigma.
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
	type diff struct {
		key  string
		f    *bandFit
		z, d float64
	}
	var ds []diff
	for _, key := range slices.Sorted(maps.Keys(fits)) {
		f := fits[key]
		if f.core[0] < minCore || f.core[1] < minCore || !(f.diffErr > 0) || f.depths[0].Value <= 0 || f.depths[1].Value <= 0 {
			continue
		}
		d := f.depths[0].Value - f.depths[1].Value
		ds = append(ds, diff{key: key, f: f, z: math.Abs(d) / f.diffErr, d: d})
	}
	slices.SortStableFunc(ds, func(a, b diff) int { return cmp.Compare(b.z, a.z) })
	var sharpest []diff
	if len(ds) > 0 {
		sharpest = []diff{slices.MinFunc(ds, func(a, b diff) int { return cmp.Compare(a.f.diffErr, b.f.diffErr) })}
	}
	if len(ds) > 1 {
		same := ds[:1]
		for _, x := range ds[1:] {
			if (x.d > 0) == (ds[0].d > 0) {
				same = append(same, x)
			}
		}
		ds = sharpest
		if len(same) > 1 {
			ds = same[1:2]
		}
	}
	if len(ds) > 0 {
		t.Band, t.Depth1, t.Depth2, t.DepthZ = ds[0].key, ds[0].f.depths[0], ds[0].f.depths[1], ds[0].z
	}
	t.Phase2, t.PhaseErr = fitPhase2(pts, e)
	if t.PhaseErr > 0 {
		t.PhaseZ = math.Abs(t.Phase2-0.5) / t.PhaseErr
	}
	return t
}

// fitPhase2 finds the second dip's phase by the least total chi-square of
// the per band depth fits over a grid of phases. Its one sigma error is
// the larger of two: where that chi-square, scaled by its reduced value,
// rises by one, and the error from the second eclipses timed one by one
// (eventSpread). Points of one eclipse share its slow drifts and eclipses
// can disagree beyond their points' errors, as for an orbit that turns,
// which the joint chi-square alone takes for noise.
func fitPhase2(pts []point, e *period.Ephemeris) (float64, float64) {
	best, chi, ok := bestPhase2(pts, e)
	if !ok {
		return e.Eclipses[1].Phase, 0
	}
	return best, math.Max(chiError(chi, best, e.Eclipses[1].Width), eventSpread(pts, e, best))
}

// minPhaseEvents is how many second eclipses timed one by one eventSpread
// needs.
const minPhaseEvents = 3

// minEventSide is how many points a second eclipse timed on its own
// needs in the dip on each side of its middle, as the user's own
// eclipses need frames.
const minEventSide = 3

// phaseSteps is how many grid steps span the second dip's width each way.
const phaseSteps = 40

// phaseChi is the total chi-square of the depth fits with the second dip
// at a phase, and how many points it takes in.
type phaseChi func(ph float64) (float64, int)

func chiOf(pts []point, e *period.Ephemeris) phaseChi {
	base := fitBands(pts, e)
	return func(ph float64) (float64, int) {
		fits := fitBands(pts, withPhase2(e, ph))
		var sum float64
		n := 0
		for key := range base {
			f, ok := fits[key]
			if !ok {
				return math.Inf(1), 0
			}
			sum += f.chi2
			n += f.n
		}
		return sum, n
	}
}

// bestPhase2 is the second dip's best phase on the grid, refined, with
// the chi-square it was found by; ok is false when no phase fits.
func bestPhase2(pts []point, e *period.Ephemeris) (float64, phaseChi, bool) {
	w := e.Eclipses[1].Width
	ph0 := e.Eclipses[1].Phase
	chi := chiOf(pts, e)
	best, bestChi, bestN := ph0, math.Inf(1), 0
	for i := -phaseSteps; i <= phaseSteps; i++ {
		ph := ph0 + w*float64(i)/phaseSteps
		if c, n := chi(ph); c < bestChi {
			best, bestChi, bestN = ph, c, n
		}
	}
	if math.IsInf(bestChi, 1) || bestN == 0 {
		return ph0, nil, false
	}
	fine := w / phaseSteps / 8
	for range 2 {
		for _, ph := range []float64{best - fine, best + fine} {
			if c, _ := chi(ph); c < bestChi {
				best, bestChi = ph, c
			}
		}
	}
	return best, chi, true
}

// chiError is half the span of phases around best where the chi-square,
// scaled by its reduced value, stays within one of its least.
func chiError(chi phaseChi, best, w float64) float64 {
	bestChi, n := chi(best)
	scale := math.Max(bestChi/float64(max(n, 1)), 1)
	step := w / phaseSteps / 4
	var lo, hi float64
	for _, side := range []float64{-1, 1} {
		ph := best
		for range 16 * phaseSteps {
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
	return (hi - lo) / 2
}

// eventSpread is the second dip's phase error from each caught second
// eclipse timed on its own, at the depths of the joint fit at best: the
// weighted mean's error, scaled up by the eclipses' scatter about it
// beyond their errors. It is 0 with fewer than minPhaseEvents eclipses
// that pin their own phase down.
func eventSpread(pts []point, e *period.Ephemeris, best float64) float64 {
	w := e.Eclipses[1].Width
	at := withPhase2(e, best)
	fits := fitBands(pts, at)
	byEvent := map[int][]point{}
	for _, p := range pts {
		f, ok := fits[p.key]
		if !ok {
			continue
		}
		x := (p.t-e.T0)/e.Period - best
		n := math.Round(x)
		if math.Abs(x-n) >= 2*w {
			continue
		}
		p.e *= math.Sqrt(math.Max(f.chi2/float64(max(f.n-len(e.Eclipses), 1)), 1))
		if math.Abs(p.y-model(at, f.depths, p.t)) > 5*p.e {
			continue
		}
		byEvent[int(n)] = append(byEvent[int(n)], p)
	}
	var phs, ws []float64
	for _, n := range slices.Sorted(maps.Keys(byEvent)) {
		if ph, err, ok := eventPhase(byEvent[n], e, fits, best); ok {
			phs = append(phs, ph)
			ws = append(ws, 1/(err*err))
		}
	}
	if len(phs) < minPhaseEvents {
		return 0
	}
	var sw, swp float64
	for i, ph := range phs {
		sw += ws[i]
		swp += ws[i] * ph
	}
	mean := swp / sw
	var chi float64
	for i, ph := range phs {
		chi += ws[i] * (ph - mean) * (ph - mean)
	}
	return math.Sqrt(1/sw) * math.Sqrt(math.Max(chi/float64(len(phs)-1), 1))
}

// eventPhase is the second dip's phase in one eclipse's points ps alone,
// with its one sigma error where their chi-square rises by one, scaled
// by their reduced chi-square when that is above one; ok is
// false unless minEventSide points fall in the dip on each side of its
// middle at best and they bound the phase within the dip's width of it.
func eventPhase(ps []point, e *period.Ephemeris, fits map[string]*bandFit, best float64) (float64, float64, bool) {
	w := e.Eclipses[1].Width
	var before, after int
	for _, p := range ps {
		x := (p.t-e.T0)/e.Period - best
		d := x - math.Round(x)
		switch {
		case d > -w && d < 0:
			before++
		case d >= 0 && d < w:
			after++
		}
	}
	if before < minEventSide || after < minEventSide {
		return 0, 0, false
	}
	chi := func(ph float64) float64 {
		c := withPhase2(e, ph)
		var s float64
		for _, p := range ps {
			r := (p.y - model(c, fits[p.key].depths, p.t)) / p.e
			s += r * r
		}
		return s
	}
	const steps = 4 * phaseSteps
	step := w / steps
	cs := make([]float64, 2*steps+1)
	lowest := 0
	for i := range cs {
		cs[i] = chi(best + step*float64(i-steps))
		if cs[i] < cs[lowest] {
			lowest = i
		}
	}
	lo, hi := -1, -1
	for i := lowest; i >= 0; i-- {
		if cs[i]-cs[lowest] >= 1 {
			lo = i
			break
		}
	}
	for i := lowest; i < len(cs); i++ {
		if cs[i]-cs[lowest] >= 1 {
			hi = i
			break
		}
	}
	if lo < 0 || hi < 0 {
		return 0, 0, false
	}
	red := math.Sqrt(math.Max(cs[lowest]/float64(max(len(ps)-1, 1)), 1))
	return best + step*float64(lowest-steps), red * step * float64(hi-lo) / 2, true
}

// withPhase2 is e with its second dip at phase ph.
func withPhase2(e *period.Ephemeris, ph float64) *period.Ephemeris {
	c := *e
	c.Eclipses = slices.Clone(e.Eclipses)
	c.Eclipses[1].Phase = ph
	return &c
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
// limit on a second dip at mid-cycle. The light curves a series merges
// (ZTF's one per field and CCD quadrant) are first put on one zero point
// (period.AlignSrc).
func Summarize(series []lc.Series, e *period.Ephemeris) *Summary {
	if e == nil || e.Period <= 0 || len(e.Eclipses) == 0 || len(e.Eclipses) > 2 {
		return nil
	}
	series = period.AlignSrc(series, e)
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

// TwinOf compares the two dips of e in the survey series, as Summarize
// does; nil without two dips or points.
func TwinOf(series []lc.Series, e *period.Ephemeris) *Twin {
	if e == nil || e.Period <= 0 || len(e.Eclipses) != 2 {
		return nil
	}
	pts := points(period.AlignSrc(series, e), e)
	if len(pts) == 0 {
		return nil
	}
	return twin(pts, e)
}

// Doubled is e at twice its period with its one dip on both halves, for a
// star that shows one dip per cycle; nil for any other.
func Doubled(e *period.Ephemeris) *period.Ephemeris { return doubled(e) }

// InEclipse says which dip of e covers bjd and how deep in its shape, 0
// to 1; dip -1 outside every dip.
func InEclipse(e *period.Ephemeris, bjd float64) (int, float64) { return inEclipse(e, bjd) }
