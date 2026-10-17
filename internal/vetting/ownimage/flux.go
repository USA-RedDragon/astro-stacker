package ownimage

import (
	"math"
	"sort"

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
// faintest extra light that would have been, three times the error.
type FluxNight struct {
	Night   string  `json:"night"`
	Filter  string  `json:"filter"`
	N       int     `json:"n"`
	Flux    float64 `json:"flux"`
	Err     float64 `json:"err"`
	Mag     float64 `json:"mag,omitempty"`
	Limit   float64 `json:"limit,omitempty"`
	Flagged bool    `json:"flagged"`
}

// SNR is the night's extra light over its error.
func (n FluxNight) SNR() float64 {
	if !(n.Err > 0) {
		return 0
	}
	return n.Flux / n.Err
}

// FluxCurve is a spot's light with no catalogued star there, fitted
// with the star shape on every sub against Comps steady stars whose Gaia
// G sets each filter's magnitude Zero; Base is each filter's level on
// the nights not flagged, from BaseN subs.
type FluxCurve struct {
	Comps  int                `json:"comps"`
	Points []FluxPoint        `json:"points"`
	Nights []FluxNight        `json:"nights"`
	Zero   map[string]float64 `json:"zero"`
	Base   map[string]float64 `json:"base"`
	BaseN  map[string]int     `json:"base_n"`
}

const (
	fluxSeen = 3.0
	// AllFilters names a night's light combined over its filters, each
	// put on the comparison stars' Gaia G scale.
	AllFilters = "all filters"
	fluxZero   = 25.0
)

// buildFluxCurve measures the spot's light in every sub less the
// filter's master, when no catalogued star sits there: what the master
// shows there, a galaxy's glow or a faint star, is taken away before the
// star shape is fitted.
func buildFluxCurve(subs []Sub, sp *spotPlan, e *ensemble) *FluxCurve {
	c := &FluxCurve{Comps: len(e.kept), Zero: map[string]float64{}, Base: map[string]float64{}, BaseN: map[string]int{}}
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
		if r == nil || !ok || !(t > 0) || !r.stamp.ok {
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
		c.Points = append(c.Points, FluxPoint{FrameID: s.FrameID, BJD: lc.BJD(midTime(s), sp.spot.RA, sp.spot.Dec), Night: nightKey(s.Night), Filter: s.Filter,
			Flux: fl / t, Err: fe / t})
	}
	if len(c.Points) == 0 {
		return nil
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
	c.Nights = fluxNights(c, sp, scatter)
	c.Nights = append(c.Nights, combineNights(c)...)
	return c
}

// combineNights joins each night's filters on the comparison stars'
// Gaia G scale, weighting each by its error.
func combineNights(c *FluxCurve) []FluxNight {
	type acc struct {
		sw, swf float64
		n, k    int
		flagged bool
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
			a = &acc{}
			by[n.Night] = a
			order = append(order, n.Night)
		}
		a.sw += 1 / (e * e)
		a.swf += f / (e * e)
		a.n += n.N
		a.k++
		a.flagged = a.flagged || n.Flagged
	}
	var out []FluxNight
	for _, night := range order {
		a := by[night]
		if a.k < 2 {
			continue
		}
		f, e := a.swf/a.sw, 1/math.Sqrt(a.sw)
		nl := FluxNight{Night: night, Filter: AllFilters, N: a.n, Flux: f, Err: e, Flagged: a.flagged, Limit: fluxZero - 2.5*math.Log10(fluxSeen*e)}
		if f >= fluxSeen*e {
			nl.Mag = fluxZero - 2.5*math.Log10(f)
		}
		out = append(out, nl)
	}
	return out
}
func fluxNights(c *FluxCurve, sp *spotPlan, scatter map[string]float64) []FluxNight {
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
	out := make([]FluxNight, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		v := make([]float64, len(g))
		errs := make([]float64, len(g))
		for i, p := range g {
			v[i], errs[i] = p.Flux-c.Base[k.filter], p.Err
		}
		m, sd, n := clippedMean(v)
		e := median(errs) / math.Sqrt(float64(n))
		if n >= 3 && sd/math.Sqrt(float64(n)) > e {
			e = sd / math.Sqrt(float64(n))
		}
		if s, ok := scatter[k.filter]; ok && finite(s) {
			e = math.Max(e, s/math.Sqrt(float64(n)))
		}
		nl := FluxNight{Night: k.night, Filter: k.filter, N: len(g), Flux: m, Err: e, Flagged: sp.flagged[k.night]}
		if zp, ok := c.Zero[k.filter]; ok && e > 0 {
			if m >= fluxSeen*e {
				nl.Mag = zp - 2.5*math.Log10(m)
			}
			nl.Limit = zp - 2.5*math.Log10(fluxSeen*e)
		}
		out = append(out, nl)
	}
	return out
}
