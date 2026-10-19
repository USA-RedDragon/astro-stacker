package ownimage

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// FluxPoint is one sub's light at a spot with no catalogued star, in the
// comparison stars' units at their usual transparency.
type FluxPoint struct {
	FrameID int     `json:"frame_id"`
	BJD     float64 `json:"bjd"`
	Night   string  `json:"night"`
	Filter  string  `json:"filter"`
	Flux    float64 `json:"flux"`
	Err     float64 `json:"err"`
}

// FluxNight is one night's light at the spot in one filter above the
// spot's level on the nights not flagged: Mag is the magnitude of that
// extra light when it is three times its error or more, and Limit the
// faintest extra light that would have been, three times the error. Err
// is the largest of the subs' own error, the spot's scatter from night to
// night and the local error.
type FluxNight struct {
	Night   string  `json:"night"`
	Filter  string  `json:"filter"`
	N       int     `json:"n"`
	Flux    float64 `json:"flux"`
	Err     float64 `json:"err"`
	Mag     float64 `json:"mag,omitempty"`
	Limit   float64 `json:"limit,omitempty"`
	Flagged bool    `json:"flagged"`
	// Local is the scatter of the same measurement at places 10 to 45 px
	// around the spot that night, 0 when they were not measured, and
	// Empty99 the 99th percentile of those places' light above their
	// median.
	Local   float64 `json:"local,omitempty"`
	Empty99 float64 `json:"empty_99,omitempty"`
	// Thin marks a night of fewer than thinSubs subs.
	Thin bool `json:"thin,omitempty"`
	// FreshN counts the night's subs the detection did not measure, and
	// FreshFlux and FreshErr are their light, when there are thinSubs or
	// more.
	FreshN    int     `json:"fresh_n,omitempty"`
	FreshFlux float64 `json:"fresh_flux,omitempty"`
	FreshErr  float64 `json:"fresh_err,omitempty"`
}

// SNR is the night's extra light over its error.
func (n FluxNight) SNR() float64 {
	if !(n.Err > 0) {
		return 0
	}
	return n.Flux / n.Err
}

// FreshSNR is the light of the subs the detection did not measure over
// its error.
func (n FluxNight) FreshSNR() float64 {
	if !(n.FreshErr > 0) {
		return 0
	}
	return n.FreshFlux / n.FreshErr
}

// Ring is the light on the circle through the spot around a galaxy's
// nucleus SepPx away.
type Ring struct {
	SepPx  float64     `json:"sep_px"`
	Nights []RingNight `json:"nights"`
}

// RingNight is one flagged night's signal to noise at the spot and at the
// brightest of the other places on the ring, both without the spot's
// usual level taken away.
type RingNight struct {
	Night  string  `json:"night"`
	Filter string  `json:"filter"`
	Spot   float64 `json:"spot"`
	Max    float64 `json:"max"`
}

// FluxCurve is a spot's light with no catalogued star there, fitted
// with the star shape on every sub against Comps steady stars whose Gaia
// G sets each filter's magnitude Zero; Base is each filter's level on
// the nights not flagged, from BaseN subs, and Floor the scatter of its
// night levels on quiet nights.
type FluxCurve struct {
	Comps  int                `json:"comps"`
	Points []FluxPoint        `json:"points"`
	Nights []FluxNight        `json:"nights"`
	Zero   map[string]float64 `json:"zero"`
	Base   map[string]float64 `json:"base"`
	BaseN  map[string]int     `json:"base_n"`
	Floor  map[string]float64 `json:"floor,omitempty"`
	Ring   *Ring              `json:"ring,omitempty"`
	Wing   *Wing              `json:"wing,omitempty"`
	Halo   *Halo              `json:"halo,omitempty"`
	// EdgePx is how far the spot is from the covered area's edge, up to
	// 150 px, when EdgeOK.
	EdgePx float64 `json:"edge_px,omitempty"`
	EdgeOK bool    `json:"edge_ok,omitempty"`
	// Scale is the frame's pixel scale, in arcsec.
	Scale float64 `json:"scale,omitempty"`
	// HotSubs counts the subs left out for a master dark pixel under the
	// spot hotDarkZ times its spread or more.
	HotSubs int `json:"hot_subs,omitempty"`
	// Resp is, per filter, what the measurement gives for a star of unit
	// light put in at the spot with the subs' own star width: a fake of
	// any light L shows as L times it.
	Resp map[string]float64 `json:"resp,omitempty"`
}

// RespOf is the measurement's response in a filter, or the median of
// all filters' for all filters combined or a filter not measured.
func (c *FluxCurve) RespOf(filter string) float64 {
	if r, ok := c.Resp[filter]; ok && filter != AllFilters {
		return r
	}
	v := make([]float64, 0, len(c.Resp))
	for _, r := range c.Resp {
		v = append(v, r)
	}
	if len(v) == 0 {
		return 1
	}
	return median(v)
}

const (
	fluxSeen = 3.0
	// AllFilters names a night's light combined over its filters, each
	// put on the comparison stars' Gaia G scale.
	AllFilters = "all filters"
	// AllFiltersZero is the magnitude zero point of AllFilters nights.
	AllFiltersZero = 25.0
	fluxZero       = AllFiltersZero
	// thinSubs is the fewest subs a night needs to stand on its own: the
	// run of Andromeda's 2025-01-23 was skipped with 1 Green and 1 Blue
	// sub, which vetting still read as light at 66053.
	thinSubs = 3
	// floorNights is the fewest quiet nights that give a floor.
	floorNights = 5
)

// buildFluxCurve measures the spot's light in every sub less the
// filter's master, when no catalogued star sits there, leaving out the
// frames skip: what the master shows there, a galaxy's glow or a faint
// star, is taken away before the star shape is fitted.
func buildFluxCurve(subs []Sub, sp *spotPlan, e *ensemble, skip map[int]bool) *FluxCurve {
	c := &FluxCurve{Comps: len(e.kept), Zero: map[string]float64{}, Base: map[string]float64{}, BaseN: map[string]int{}, Floor: map[string]float64{}, HotSubs: len(skip), Scale: sp.scale,
		Resp: map[string]float64{}}
	resps := map[string][]float64{}
	zps := map[string][]float64{}
	for f, med := range e.medians {
		for _, j := range e.kept {
			if j < len(sp.comps) && med[j] > 0 && sp.comps[j].g > 0 {
				zps[f] = append(zps[f], sp.comps[j].g+2.5*math.Log10(med[j]))
			}
		}
	}
	for f, v := range zps {
		c.Zero[f] = median(v)
	}
	cx, cy := stampHalf+sp.x-math.Round(sp.x), stampHalf+sp.y-math.Round(sp.y)
	for i, r := range sp.recs {
		t, ok := e.transp[i]
		if r == nil || !ok || !(t > 0) || !r.stamp.ok || skip[subs[i].FrameID] {
			continue
		}
		s := subs[i]
		ms := sp.masters[s.Filter]
		if ms == nil {
			continue
		}
		width := r.sigma
		if !(width > 0) {
			width = sp.mSigma[s.Filter]
		}
		fl, fe, ok := psfFlux(diffOf(sp.stamps.get(r.stamp), ms, r.sigma, sp.mSigma[s.Filter], false), cx, cy, width)
		if !ok {
			continue
		}
		if rv, rok := unitResponse(cx, cy, width); rok {
			resps[s.Filter] = append(resps[s.Filter], rv)
		}
		c.Points = append(c.Points, FluxPoint{FrameID: s.FrameID, BJD: lc.BJD(midTime(s), sp.spot.RA, sp.spot.Dec), Night: nightKey(s.Night), Filter: s.Filter,
			Flux: fl / t, Err: fe / t})
	}
	c.Points = dropDegenerate(c.Points, c.Zero)
	if len(c.Points) == 0 {
		return nil
	}
	for f, v := range resps {
		c.Resp[f] = median(v)
	}
	sort.Slice(c.Points, func(a, b int) bool { return c.Points[a].BJD < c.Points[b].BJD })
	first := ""
	for k := range sp.flagged {
		if first == "" || k < first {
			first = k
		}
	}
	before, normal := map[string][]float64{}, map[string][]float64{}
	for _, p := range c.Points {
		if sp.flagged[p.Night] {
			continue
		}
		normal[p.Filter] = append(normal[p.Filter], p.Flux)
		if p.Night < first {
			before[p.Filter] = append(before[p.Filter], p.Flux)
		}
	}
	scatter := map[string]float64{}
	for f, v := range normal {
		if b := before[f]; len(b) >= minPresent {
			v = b
		}
		if len(v) >= minPresent {
			c.Base[f], c.BaseN[f], scatter[f] = median(v), len(v), robustStd(v)
		}
	}
	nightsOf(c, subs, sp, e, skip, scatter)
	return c
}

// degenerateErr is the least share of the curve's median error a sub's
// error may have, both put on the comparison stars' Gaia G scale. Subs
// are weighted by the inverse square of their error; on that scale the
// subs' errors differ by about 5 times between filters (M 15: 58 to 95;
// Cygnus Loop Panel 2: 45 to 200) and by the square root of the exposure
// within one. On M 15's 2026-10-18 one Green sub stood at an error of
// 1.2e-4 there, its stamp the 2-sub Green master's own; it outweighed the
// night's 12 other subs, and the stack read the fake as magnitude 32.2
// and 0.0 in the night's unused frames.
const degenerateErr = 0.01

// dropDegenerate leaves out the points whose error, on the zero points'
// common scale, is under degenerateErr of the median.
func dropDegenerate(pts []FluxPoint, zero map[string]float64) []FluxPoint {
	common := func(p FluxPoint) (float64, bool) {
		zp, ok := zero[p.Filter]
		return p.Err * math.Pow(10, 0.4*(fluxZero-zp)), ok
	}
	var errs []float64
	for _, p := range pts {
		if e, ok := common(p); ok {
			errs = append(errs, e)
		}
	}
	if len(errs) == 0 {
		return pts
	}
	lim := degenerateErr * median(errs)
	out := pts[:0]
	for _, p := range pts {
		if e, ok := common(p); !ok || e >= lim {
			out = append(out, p)
		}
	}
	return out
}

// nightsOf fills a curve's nights, single filters and all combined, and
// what the bare spot's places around it show.
func nightsOf(c *FluxCurve, subs []Sub, sp *spotPlan, e *ensemble, skip map[int]bool, scatter map[string]float64) {
	var pns []probeNight
	used := map[int]bool{}
	ring := 0
	if sp.bare != nil {
		pns, used, ring = probeNights(subs, sp, e, skip), sp.bare.used, sp.bare.ring
	}
	c.Nights = fluxNights(c, sp, scatter, pns, used)
	all := combineNights(c, pns, ring)
	if fl := floorOf(all, sp); finite(fl) {
		c.Floor[AllFilters] = fl
		for i := range all {
			all[i].Err = math.Max(all[i].Err, fl)
			if all[i].FreshErr > 0 {
				all[i].FreshErr = math.Max(all[i].FreshErr, fl)
			}
			setMag(&all[i], nil, AllFilters)
		}
	}
	c.Nights = append(c.Nights, all...)
	if sp.bare != nil {
		c.Ring = ringNights(c, sp, pns)
		if sp.bare.wing != nil {
			c.Wing = wingNights(subs, sp, e)
		}
		c.Halo, c.EdgePx, c.EdgeOK = sp.bare.halo, sp.bare.edge, sp.bare.edgeOK
	}
}

// nightMean is the clipped mean of a night's subs and its error: the
// subs' own errors over the root of their count, or their scatter when
// larger, and never under the filter's single sub scatter on normal
// nights over the same root.
func nightMean(v, errs []float64, scatter float64) (m, e float64) {
	m, sd, n := clippedMean(v)
	e = median(errs) / math.Sqrt(float64(n))
	if n >= 3 && sd/math.Sqrt(float64(n)) > e {
		e = sd / math.Sqrt(float64(n))
	}
	if finite(scatter) {
		e = math.Max(e, scatter/math.Sqrt(float64(n)))
	}
	return m, e
}

// floorOf is the scatter of a filter's night levels on quiet nights: not
// flagged, nor the nights just after, with thinSubs subs or more; those
// before the first flagged night when there are enough, else those
// wingFarDays or more from every flagged night, else all.
func floorOf(levels []FluxNight, sp *spotPlan) float64 {
	first := ""
	var flagged []time.Time
	for k := range sp.flagged {
		if first == "" || k < first {
			first = k
		}
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
	var pre, away, all []float64
	for _, n := range levels {
		if n.Flagged || n.N < thinSubs || (sp.bare != nil && sp.bare.near[n.Night]) {
			continue
		}
		all = append(all, n.Flux)
		if n.Night < first {
			pre = append(pre, n.Flux)
		}
		if far(n.Night) {
			away = append(away, n.Flux)
		}
	}
	for _, v := range [][]float64{pre, away, all} {
		if len(v) >= floorNights {
			return robustStd(v)
		}
	}
	return math.NaN()
}

func fluxNights(c *FluxCurve, sp *spotPlan, scatter map[string]float64, pns []probeNight, used map[int]bool) []FluxNight {
	type key struct{ night, filter string }
	groups := map[key][]FluxPoint{}
	var keys []key
	for _, p := range c.Points {
		k := key{p.Night, p.Filter}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], p)
	}
	local, tail := map[key]float64{}, map[key]float64{}
	if sp.bare != nil {
		for _, pn := range pns {
			if s := sp.bare.emptyScatter(pn.v); finite(s) {
				local[key{pn.night, pn.filter}] = s
				tail[key{pn.night, pn.filter}] = sp.bare.emptyTail(pn.v)
			}
		}
	}
	out := make([]FluxNight, 0, len(keys))
	byFilter := map[string][]int{}
	for _, k := range keys {
		g := groups[k]
		var v, errs, fv, fe []float64
		for _, p := range g {
			v, errs = append(v, p.Flux-c.Base[k.filter]), append(errs, p.Err)
			if !used[p.FrameID] {
				fv, fe = append(fv, p.Flux-c.Base[k.filter]), append(fe, p.Err)
			}
		}
		m, e := nightMean(v, errs, scatter[k.filter])
		nl := FluxNight{Night: k.night, Filter: k.filter, N: len(g), Flux: m, Err: e, Flagged: sp.flagged[k.night], Thin: len(g) < thinSubs,
			Local: local[k], Empty99: tail[k], FreshN: len(fv)}
		if len(fv) >= thinSubs {
			nl.FreshFlux, nl.FreshErr = nightMean(fv, fe, scatter[k.filter])
			if nl.Local > 0 {
				nl.FreshErr = math.Max(nl.FreshErr, nl.Local*math.Sqrt(float64(len(g))/float64(len(fv))))
			}
		}
		byFilter[k.filter] = append(byFilter[k.filter], len(out))
		out = append(out, nl)
	}
	for f, idx := range byFilter {
		levels := make([]FluxNight, len(idx))
		for j, i := range idx {
			levels[j] = out[i]
		}
		fl := floorOf(levels, sp)
		if finite(fl) {
			c.Floor[f] = fl
		}
		for _, i := range idx {
			n := &out[i]
			n.Err = math.Max(n.Err, n.Local)
			if finite(fl) {
				n.Err = math.Max(n.Err, fl)
				if n.FreshErr > 0 {
					n.FreshErr = math.Max(n.FreshErr, fl)
				}
			}
			setMag(n, c.Zero, n.Filter)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Night < out[b].Night })
	return out
}

func setMag(n *FluxNight, zero map[string]float64, filter string) {
	zp, ok := zero[filter]
	if filter == AllFilters {
		zp, ok = fluxZero, true
	}
	n.Mag, n.Limit = 0, 0
	if !ok || !(n.Err > 0) {
		return
	}
	if n.Flux >= fluxSeen*n.Err {
		n.Mag = zp - 2.5*math.Log10(n.Flux)
	}
	n.Limit = zp - 2.5*math.Log10(fluxSeen*n.Err)
}

// combineNights joins each night's filters on the comparison stars'
// Gaia G scale, weighting each by its error, and the probes the same way
// for the night's local error.
func combineNights(c *FluxCurve, pns []probeNight, ring int) []FluxNight {
	type acc struct {
		sw, swf     float64
		fsw, fswf   float64
		n, k, fresh int
		most        int
		flagged     bool
		weights     map[string]float64
	}
	by := map[string]*acc{}
	var order []string
	for _, n := range c.Nights {
		zp, ok := c.Zero[n.Filter]
		if !ok || !(n.Err > 0) {
			continue
		}
		k := math.Pow(10, 0.4*(fluxZero-zp))
		f, e := n.Flux*k, n.Err*k
		a := by[n.Night]
		if a == nil {
			a = &acc{weights: map[string]float64{}}
			by[n.Night] = a
			order = append(order, n.Night)
		}
		a.sw += 1 / (e * e)
		a.swf += f / (e * e)
		a.weights[n.Filter] = k / (e * e)
		a.n += n.N
		a.most = max(a.most, n.N)
		a.k++
		a.flagged = a.flagged || n.Flagged
		if n.FreshErr > 0 {
			fe := n.FreshErr * k
			a.fsw += 1 / (fe * fe)
			a.fswf += n.FreshFlux * k / (fe * fe)
			a.fresh += n.FreshN
		}
	}
	probes := map[string]map[string][]float64{}
	for _, pn := range pns {
		if probes[pn.night] == nil {
			probes[pn.night] = map[string][]float64{}
		}
		probes[pn.night][pn.filter] = pn.v
	}
	var out []FluxNight
	for _, night := range order {
		a := by[night]
		if a.k < 2 {
			continue
		}
		f, e := a.swf/a.sw, 1/math.Sqrt(a.sw)
		nl := FluxNight{Night: night, Filter: AllFilters, N: a.n, Flux: f, Err: e, Flagged: a.flagged, Thin: a.most < thinSubs, FreshN: a.fresh}
		if a.fsw > 0 {
			nl.FreshFlux, nl.FreshErr = a.fswf/a.fsw, 1/math.Sqrt(a.fsw)
		}
		if l, t := combinedLocal(probes, night, a.weights, a.sw, ring); l > 0 {
			nl.Local, nl.Empty99 = l, t
			nl.Err = math.Max(nl.Err, l)
		}
		setMag(&nl, nil, AllFilters)
		out = append(out, nl)
	}
	return out
}

// combinedLocal is the local error of a night's filters joined with the
// weights they had, from the empty probes measured in every one of them,
// and the 99th percentile of the probes above their median.
func combinedLocal(probes map[string]map[string][]float64, night string, weights map[string]float64, sw float64, ring int) (float64, float64) {
	var vs [][]float64
	var ws []float64
	for f, w := range weights {
		v, ok := probes[night][f]
		if !ok {
			return 0, 0
		}
		vs, ws = append(vs, v), append(ws, w)
	}
	if len(vs) == 0 {
		return 0, 0
	}
	var all []float64
	for j := 1 + ring; j < len(vs[0]); j++ {
		var s float64
		ok := true
		for i, v := range vs {
			if !finite(v[j]) {
				ok = false
				break
			}
			s += ws[i] * v[j]
		}
		if ok {
			all = append(all, s/sw)
		}
	}
	if len(all) < minProbes {
		return 0, 0
	}
	return robustStd(all), tailOf(all)
}

// unitResponse is what psfFlux gives for a star of unit light and width
// sigma put in at (cx, cy) of an empty stamp.
func unitResponse(cx, cy, sigma float64) (float64, bool) {
	st := make([]float32, stampSide*stampSide)
	gaussianAt(st, stampSide, cx, cy, sigma, 1)
	v := make([]float64, len(st))
	for k, x := range st {
		v[k] = float64(x)
	}
	ps := psfSumsOf(v, cx, cy, sigma)
	if !(ps.sw2 > 0) || len(ps.annulus) < psfAnnulusMin {
		return 0, false
	}
	return (ps.swv - median(ps.annulus)*ps.sw) / ps.sw2, true
}
