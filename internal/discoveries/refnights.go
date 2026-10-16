package discoveries

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"
)

// maxNightShare is the most of a reference's weight one night may hold when
// it has three nights or more; with two, each holds half. A variable star's
// eclipse night otherwise outweighs the rest: Andromeda's L reference for
// 2025-01-19 held 28 subs of 2026-10-11, the eclipse of 2MASS
// J00433343+4139446, among 38.
const maxNightShare = 1.0 / 3

// nightShares is each reference sub's share of its weight, so no night
// holds more than maxNightShare of the subs' count (half with two nights),
// or nil when none needs it. Every sub of a night over the cap is weighted
// alike, by the cap over the night's count.
func nightShares(subs []Sub) []float64 {
	count := map[time.Time]float64{}
	for _, s := range subs {
		count[s.Night]++
	}
	if len(count) < 2 {
		return nil
	}
	share := math.Max(maxNightShare, 1/float64(len(count)))
	var cs []float64
	for _, c := range count {
		cs = append(cs, c)
	}
	sort.Float64s(cs)
	capped := func(k float64) float64 {
		var t float64
		for _, c := range cs {
			t += math.Min(c, k)
		}
		return t
	}
	top := cs[len(cs)-1]
	if top <= share*capped(top) {
		return nil
	}
	lo, hi := 0.0, top
	for range 60 {
		k := (lo + hi) / 2
		if k <= share*capped(k)+1e-9 {
			lo = k
		} else {
			hi = k
		}
	}
	out := make([]float64, len(subs))
	for i, s := range subs {
		out[i] = math.Min(1, lo/count[s.Night])
	}
	return out
}

// refNightBox is the half size of the cutout a reference sub is measured
// in, and refSkyIn and refSkyOut the annulus its local sky comes from.
const (
	refNightBox = 22
	refSkyIn    = 10.0
	refSkyOut   = 20.0
)

// refNight is a change's light in one reference night's subs minus the
// reference: their mean, how many, and the night's weight in the
// reference.
type refNight struct {
	night time.Time
	mean  float64
	n     int
	w     float64
}

// measureRefNights measures each candidate in every reference sub minus
// its filter's reference, night by night.
func (in *Input) measureRefNights(ctx context.Context, stacks []*filterStacks, cands []cand) error {
	if len(cands) == 0 {
		return nil
	}
	for _, fs := range stacks {
		type acc struct{ sum, n, w float64 }
		shares := nightShares(fs.f.Ref)
		per := make([]map[time.Time]*acc, len(cands))
		for i := range per {
			per[i] = map[time.Time]*acc{}
		}
		for j, s := range fs.f.Ref {
			in.stage(fmt.Sprintf("measuring %s reference sub %d of %d", fs.f.Filter, j+1, len(fs.f.Ref)))
			p, _, err := fs.load(ctx, s)
			if errors.Is(err, errBadSub) {
				continue
			}
			if err != nil {
				return err
			}
			for ci := range cands {
				c := &cands[ci]
				r := apertureR
				if c.wide {
					r = residualR
				}
				fl := localFlux(p, fs.ref, c.X, c.Y, r)
				if math.IsNaN(fl) {
					continue
				}
				a := per[ci][s.Night]
				if a == nil {
					a = &acc{}
					per[ci][s.Night] = a
				}
				a.sum += float64(c.Sign) * fl
				a.n++
				if shares != nil {
					a.w += shares[j]
				} else {
					a.w++
				}
			}
		}
		for ci := range cands {
			c := &cands[ci]
			for nt, a := range per[ci] {
				if c.refNights == nil {
					c.refNights = map[string][]refNight{}
				}
				c.refNights[fs.f.Filter] = append(c.refNights[fs.f.Filter], refNight{night: nt, mean: a.sum / a.n, n: int(a.n), w: a.w})
			}
			if len(per[ci]) > 0 {
				r := apertureR
				if c.wide {
					r = residualR
				}
				if c.refLight == nil {
					c.refLight = map[string]float64{}
				}
				c.refLight[fs.f.Filter] = Aperture(fs.ref, c.X, c.Y, r)
			}
			sort.Slice(c.refNights[fs.f.Filter], func(a, b int) bool {
				return c.refNights[fs.f.Filter][a].night.Before(c.refNights[fs.f.Filter][b].night)
			})
		}
	}
	return nil
}

// localFlux is the light of p minus ref in an aperture of radius r at
// (x, y), less the median of p minus ref in an annulus around it; NaN when
// the annulus has too little data.
func localFlux(p, ref *Plane, x, y, r float64) float64 {
	cx, cy := int(math.Round(x)), int(math.Round(y))
	n := 2*refNightBox + 1
	q := NewPlane(n, n)
	var ring []float64
	for dy := -refNightBox; dy <= refNightBox; dy++ {
		for dx := -refNightBox; dx <= refNightBox; dx++ {
			px, py := cx+dx, cy+dy
			k := (dy+refNightBox)*n + dx + refNightBox
			if px < 0 || py < 0 || px >= p.W || py >= p.H {
				q.Data[k] = float32(math.NaN())
				continue
			}
			a, b := p.Data[py*p.W+px], ref.Data[py*p.W+px]
			if isNaN32(a) || isNaN32(b) {
				q.Data[k] = float32(math.NaN())
				continue
			}
			q.Data[k] = a - b
			if d := math.Hypot(float64(px)-x, float64(py)-y); d >= refSkyIn && d <= refSkyOut {
				ring = append(ring, float64(a-b))
			}
		}
	}
	if len(ring) < 50 {
		return math.NaN()
	}
	sky := float32(median64(ring))
	for k, v := range q.Data {
		if !isNaN32(v) {
			q.Data[k] = v - sky
		}
	}
	return Aperture(q, x-float64(cx-refNightBox), y-float64(cy-refNightBox), r)
}

// envelopeSigma is how far, in sigma, the night must stand beyond each
// reference night of two subs or more, filters combined, for a change to be
// the night's own, unless it changed by minNewStarMag from the reference
// nights' median.
const envelopeSigma = 2.0

// minEnvelopeSubs is the fewest subs a reference night needs to count.
const minEnvelopeSubs = 2

// refNightShift says which reference night already held the change, or "":
// one is the night's own where the night stands beyond every reference
// night of minEnvelopeSubs subs or more, or changed by minNewStarMag or
// more from the median of the reference nights, each weighted by its weight
// in the reference. A reference night that outweighs the others makes a
// change on a variable star's other nights: Andromeda's 2025-01-19 read
// 2MASS J00433343+4139446 0.169 mag brighter than a reference holding 28
// subs of its eclipse on 2026-10-11 among 38, and 0.071 brighter once no
// night held over a third; it stood no brighter than on 2025-01-22, and
// 0.02 mag from the median. A transient on a reference night too changed
// from the median: AT 2026aeob, as bright on 2026-10-09 as on 2026-10-08,
// by 0.3 mag of the light there. Each sub's error is the larger of its
// noise and the night's scatter of subs, which on a bright star is its
// seeing and scintillation: three times the noise there.
func (c *cand) refNightShift() string {
	if len(c.refNights) == 0 {
		return ""
	}
	type acc struct {
		fl, er []float64
	}
	per := map[string]*acc{}
	for k, pt := range c.Light {
		if k >= len(c.fluxes) {
			break
		}
		a := per[pt.Filter]
		if a == nil {
			a = &acc{}
			per[pt.Filter] = a
		}
		a.fl, a.er = append(a.fl, c.fluxes[k]), append(a.er, c.errs[k])
	}
	filters := make([]string, 0, len(per))
	for f := range per {
		filters = append(filters, f)
	}
	slices.Sort(filters)
	var envSum, medMag float64
	var nf int
	var at time.Time
	worst := math.Inf(1)
	for _, f := range filters {
		a := per[f]
		n := float64(len(a.fl))
		sub := median64(a.er)
		if len(a.fl) >= 3 {
			sub = math.Max(sub, stddev(a.fl))
		}
		if !(sub > 0) {
			continue
		}
		mean := 0.0
		for _, v := range a.fl {
			mean += v
		}
		mean /= n
		beyond := func(rn refNight) float64 { return (mean - rn.mean) / (sub * math.Sqrt(1/n+1/float64(rn.n))) }
		var rs []refNight
		zf, nt := math.Inf(1), time.Time{}
		for _, rn := range c.refNights[f] {
			if rn.n < minEnvelopeSubs {
				continue
			}
			rs = append(rs, rn)
			if z := beyond(rn); z < zf {
				zf, nt = z, rn.night
			}
		}
		if len(rs) == 0 {
			continue
		}
		medMag = math.Max(medMag, magChange(c.refLight[f], mean-medianNight(rs).mean, c.Sign))
		envSum += zf
		nf++
		if zf < worst {
			worst, at = zf, nt
		}
	}
	if nf == 0 {
		return ""
	}
	env := envSum / math.Sqrt(float64(nf))
	if env < envelopeSigma && medMag < minNewStarMag {
		return fmt.Sprintf("as %s on the reference night of %s: %.1f sigma beyond it, %.2f mag from the reference nights' median", signName(c.Sign), at.Format(time.DateOnly), env, medMag)
	}
	return ""
}

// magChange is the size in magnitudes of a change of d, in the direction of
// sign, on light ref; infinite where there is no light to change or it all
// went.
func magChange(ref, d float64, sign int) float64 {
	if !(ref > 0) {
		return math.Inf(1)
	}
	r := (ref + float64(sign)*d) / ref
	if !(r > 0) {
		return math.Inf(1)
	}
	return math.Abs(2.5 * math.Log10(r))
}

// medianNight is the night at half the reference nights' weight, by
// their mean.
func medianNight(rs []refNight) refNight {
	sorted := slices.Clone(rs)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].mean < sorted[b].mean })
	var total, cum float64
	for _, r := range sorted {
		total += r.w
	}
	for _, r := range sorted {
		cum += r.w
		if cum >= total/2 {
			return r
		}
	}
	return sorted[len(sorted)-1]
}

func stddev(v []float64) float64 {
	var m float64
	for _, x := range v {
		m += x
	}
	m /= float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(v)-1))
}
