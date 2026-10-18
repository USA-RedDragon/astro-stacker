package ownimage

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	// probeInner and probeOuter bound the empty places, in px from the
	// spot, whose scatter is a night's local error. The pixel noise is not
	// the error at a spot in a galaxy's bulge or a bright star's wing: the
	// audit of 2026-10-17 measured the scatter of the same fit at places 10
	// to 45 px away 1.5 to 3.4 times the image-wide noise in M 31's bulge,
	// and with it AT 2026acsi (source 182), a real nova, still stood at 6.2.
	probeInner = 10
	probeOuter = 45
	// probeCount is how many empty places are measured, minProbes the
	// fewest that give a local error.
	probeCount = 200
	minProbes  = 100
	wideHalf   = probeOuter + stampHalf
	wideSide   = 2*wideHalf + 1
	// followDays is how many days after a flagged night the nights are
	// measured the same way, for the light to be followed.
	followDays = 3
	// nucleusPx is how far from the spot a galaxy's nucleus is looked for,
	// nucleusMin the nearest it may be to be told from the spot, and
	// ringAngles how many places on the circle through the spot around it
	// are measured: 54004 on Markarian Chain Panel 9's 2026-02-23 sat 6.4
	// px from NGC 4313's nucleus at H-a z 7.6, while the same circle ran
	// from -3.1 to +7.0.
	nucleusPx  = 15.0
	nucleusMin = 3.0
	ringAngles = 12
	// ringClear is how far a ring place must be from the spot not to take
	// in the spot's own light: a star of width 1.4 px puts a third of it
	// into a fit 3 px away.
	ringClear = 3.5
	// plasticA and plasticB spread the empty places evenly without a
	// random source: the R2 sequence's steps.
	plasticA = 0.7548776662466927
	plasticB = 0.5698402909980532
	// nucleusSmooth is the blur, in px, that keeps a star's or noise's
	// single bright pixel from being taken for the nucleus.
	nucleusSmooth = 1.5
)

// probe is a place measured the same way as the spot, as an offset from
// the spot's rounded pixel.
type probe struct{ dx, dy float64 }

// barePlan is what only a spot with no catalogued star needs.
type barePlan struct {
	// probes are the spot itself, ring places around a nucleus, then
	// empty places.
	probes []probe
	ring   int
	// nucleus is the nucleus's offset from the spot's rounded pixel.
	nucleus    *probe
	nucleusSep float64
	// near are the nights the probes are measured on: the flagged ones
	// and the followDays after each.
	near map[string]bool
	// wide are each filter's master over the probes' places.
	wide map[string][]float32
	// edge is how far the spot is from the covered area's edge, up to
	// clearEdgePx.
	edge   float64
	edgeOK bool
	halo   *Halo
	wing   *wingPlan
	// used are the frames the detection measured.
	used map[int]bool
}

func newBarePlan(sp *spotPlan, subs []Sub) *barePlan {
	b := &barePlan{near: map[string]bool{}, wide: map[string][]float32{}, used: map[int]bool{}}
	for k := range sp.flagged {
		d, err := time.Parse(dateLayout, k)
		if err != nil {
			continue
		}
		for i := 0; i <= followDays; i++ {
			b.near[d.AddDate(0, 0, i).Format(dateLayout)] = true
		}
	}
	for _, n := range sp.spot.Nights {
		nk := nightKey(n.Night)
		if len(n.Light) > 0 {
			for _, l := range n.Light {
				b.used[l.FrameID] = true
			}
			continue
		}
		for _, s := range subs {
			if nightKey(s.Night) == nk && discoveries.GroupOf(s.Filter) == n.Group && (n.RunAt.IsZero() || s.Start.Before(n.RunAt)) {
				b.used[s.FrameID] = true
			}
		}
	}
	fx, fy := sp.x-math.Round(sp.x), sp.y-math.Round(sp.y)
	b.probes = append(b.probes, probe{fx, fy})
	seen := map[[2]int]bool{}
	var empties []probe
	for i := 1; len(empties) < probeCount && i < 50*probeCount; i++ {
		u, v := math.Mod(float64(i)*plasticA, 1), math.Mod(float64(i)*plasticB, 1)
		dx, dy := int(math.Round((2*u-1)*probeOuter)), int(math.Round((2*v-1)*probeOuter))
		r := math.Hypot(float64(dx), float64(dy))
		if r < probeInner || r > probeOuter || seen[[2]int{dx, dy}] {
			continue
		}
		seen[[2]int{dx, dy}] = true
		empties = append(empties, probe{float64(dx) + fx, float64(dy) + fy})
	}
	b.probes = append(b.probes, empties...)
	return b
}

// setNucleus finds the brightest place of a master within nucleusPx of
// the spot, a galaxy's nucleus when there is one, and puts the ring of
// places through the spot around it among the probes.
func (b *barePlan) setNucleus(sp *spotPlan, master []float32) {
	v := make([]float64, len(master))
	for k, x := range master {
		v[k] = float64(x)
	}
	v = blur(v, wideSide, nucleusSmooth)
	fx, fy := sp.x-math.Round(sp.x), sp.y-math.Round(sp.y)
	best, bk := math.Inf(-1), -1
	for k, x := range v {
		dx, dy := float64(k%wideSide-wideHalf), float64(k/wideSide-wideHalf)
		d := math.Hypot(dx-fx, dy-fy)
		if d > nucleusPx || !finite(x) || x <= best {
			continue
		}
		best, bk = x, k
	}
	if bk < 0 {
		return
	}
	ix, iy := bk%wideSide, bk/wideSide
	if ix == 0 || iy == 0 || ix == wideSide-1 || iy == wideSide-1 {
		return
	}
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if w := v[(iy+dy)*wideSide+ix+dx]; (dx != 0 || dy != 0) && !(w < best) {
				return
			}
		}
	}
	nx, ny := float64(ix-wideHalf), float64(iy-wideHalf)
	sep := math.Hypot(nx-fx, ny-fy)
	if sep < nucleusMin || math.Hypot(nx-fx, ny-fy) > nucleusPx {
		return
	}
	b.nucleus, b.nucleusSep = &probe{nx, ny}, sep
	a0 := math.Atan2(fy-ny, fx-nx)
	var ring []probe
	for k := 1; k < ringAngles; k++ {
		a := a0 + 2*math.Pi*float64(k)/ringAngles
		p := probe{nx + sep*math.Cos(a), ny + sep*math.Sin(a)}
		if math.Hypot(p.dx-fx, p.dy-fy) >= ringClear {
			ring = append(ring, p)
		}
	}
	if len(ring) == 0 {
		b.nucleus = nil
		return
	}
	b.ring = len(ring)
	b.probes = append(b.probes[:1], append(ring, b.probes[1:]...)...)
}

// cutWide cuts a wideSide square of a plane around the spot's rounded
// pixel.
func cutWide(p *discoveries.Plane, sp *spotPlan) []float32 {
	cx, cy := int(math.Round(sp.x)), int(math.Round(sp.y))
	out := make([]float32, wideSide*wideSide)
	for j := range wideSide {
		for i := range wideSide {
			out[j*wideSide+i] = p.At(cx-wideHalf+i, cy-wideHalf+j)
		}
	}
	return out
}

// probeFluxes measures every probe in one sub less the filter's master,
// the same way the spot is measured: each place's stamp less the master's,
// fitted with the star shape. NaN marks a place that could not be
// measured.
func probeFluxes(sub, master []float32, sigSub, sigRef, width float64, probes []probe) []float32 {
	m := make([]float64, len(master))
	for k, x := range master {
		m[k] = float64(x)
	}
	m = blurTo(m, wideSide, sigSub, sigRef)
	out := make([]float32, len(probes))
	s := make([]float64, stampSide*stampSide)
	r := make([]float64, stampSide*stampSide)
	for i, pr := range probes {
		out[i] = float32(math.NaN())
		ix, iy := int(math.Round(pr.dx)), int(math.Round(pr.dy))
		if ix-stampHalf < -wideHalf || iy-stampHalf < -wideHalf || ix+stampHalf > wideHalf || iy+stampHalf > wideHalf {
			continue
		}
		for j := range stampSide {
			for k := range stampSide {
				w := (iy-stampHalf+j+wideHalf)*wideSide + ix - stampHalf + k + wideHalf
				s[j*stampSide+k], r[j*stampSide+k] = float64(sub[w]), m[w]
			}
		}
		d := subtractRef(s, r, false)
		if d == nil {
			continue
		}
		cx, cy := float64(stampHalf)+pr.dx-float64(ix), float64(stampHalf)+pr.dy-float64(iy)
		if f, _, ok := psfFlux(d, cx, cy, width); ok {
			out[i] = float32(f)
		}
	}
	return out
}

// probeNight is one night's level at every probe in one filter, each the
// clipped mean of its subs over their transparency.
type probeNight struct {
	night, filter string
	n             int
	v             []float64
}

// probeNights gathers the probes' night levels.
func probeNights(subs []Sub, sp *spotPlan, e *ensemble, skip map[int]bool) []probeNight {
	b := sp.bare
	type key struct{ night, filter string }
	groups := map[key][]int{}
	var keys []key
	for i, r := range sp.recs {
		t, ok := e.transp[i]
		if r == nil || r.probes == nil || !ok || !(t > 0) || skip[subs[i].FrameID] {
			continue
		}
		k := key{nightKey(subs[i].Night), subs[i].Filter}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	sort.Slice(keys, func(a, c int) bool {
		if keys[a].night != keys[c].night {
			return keys[a].night < keys[c].night
		}
		return keys[a].filter < keys[c].filter
	})
	out := make([]probeNight, 0, len(keys))
	for _, k := range keys {
		idx := groups[k]
		pn := probeNight{night: k.night, filter: k.filter, n: len(idx), v: make([]float64, len(b.probes))}
		col := make([]float64, 0, len(idx))
		for j := range b.probes {
			col = col[:0]
			for _, i := range idx {
				if v := sp.recs[i].probes[j]; !isNaN32(v) {
					col = append(col, float64(v)/e.transp[i])
				}
			}
			pn.v[j] = math.NaN()
			if len(col) > 0 {
				pn.v[j], _, _ = clippedMean(col)
			}
		}
		out = append(out, pn)
	}
	return out
}

// emptyScatter is the robust scatter of the empty probes' levels, NaN
// with too few.
func (b *barePlan) emptyScatter(v []float64) float64 {
	var e []float64
	for _, x := range v[1+b.ring:] {
		if finite(x) {
			e = append(e, x)
		}
	}
	if len(e) < minProbes {
		return math.NaN()
	}
	return robustStd(e)
}

// ringNights is, on each flagged night and filter, the spot's signal to
// noise against the brightest place on the ring around a nucleus, both
// on the night's error.
func ringNights(c *FluxCurve, sp *spotPlan, pns []probeNight) *Ring {
	b := sp.bare
	if b.nucleus == nil || b.ring == 0 {
		return nil
	}
	errs := map[string]float64{}
	for _, n := range c.Nights {
		errs[n.Night+"\x00"+n.Filter] = n.Err
	}
	out := &Ring{SepPx: b.nucleusSep}
	for _, pn := range pns {
		e := errs[pn.night+"\x00"+pn.filter]
		if !sp.flagged[pn.night] || !(e > 0) || !finite(pn.v[0]) {
			continue
		}
		hi := math.Inf(-1)
		for _, v := range pn.v[1 : 1+b.ring] {
			if finite(v) {
				hi = math.Max(hi, v)
			}
		}
		if math.IsInf(hi, -1) {
			continue
		}
		out.Nights = append(out.Nights, RingNight{Night: pn.night, Filter: pn.filter, Spot: pn.v[0] / e, Max: hi / e})
	}
	return out
}
