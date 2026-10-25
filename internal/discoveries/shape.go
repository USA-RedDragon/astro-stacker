package discoveries

import (
	"fmt"
	"math"
	"slices"
)

const (
	// shapeStars is how many of a filter's anchor stars, the brightest
	// first, a sub's star shape is measured on, and shapeMinStars the
	// fewest that must be measured.
	shapeStars    = 40
	shapeMinStars = 5
	// elongPercentile is the share of the reference's subs whose star axis
	// ratio a sub's must exceed to be elongated. With the subs a change is
	// in drawn from the reference's own spread, more than half of 3 of them
	// stand above its 90th percentile 2.8% of the time, of 5 0.9%, of 7
	// 0.3%. Its 95th would miss Cygnis Loop Panel 1's 2026-09-02: there
	// 9.4% of the other nights' frames reached the axis ratio 1.29 of its
	// elongated stretch.
	elongPercentile = 0.9
	// elongMinRatio is the least median axis ratio of a change's subs that
	// counts as elongated, whatever the reference's spread.
	elongMinRatio = 1.15
	elongMinSubs  = 3
)

// starShape is the median axis ratio of the anchor stars of a sub,
// background subtracted, and the angle of their long axis in degrees from
// +x toward +y, from the second moments of the positive pixels within r of
// each star's centroid. Both are NaN where under shapeMinStars stars could
// be measured.
func starShape(p *Plane, anchors []Anchor, r float64) (ratio, angle float64) {
	var ratios []float64
	var cx, cy float64
	for _, a := range anchors[:min(len(anchors), shapeStars)] {
		mx, my, ok := momentCentre(p, a.X, a.Y, r)
		if !ok {
			continue
		}
		xx, yy, xy, ok := moments(p, mx, my, r)
		if !ok {
			continue
		}
		tr, det := xx+yy, xx*yy-xy*xy
		disc := math.Sqrt(math.Max(tr*tr/4-det, 0))
		l1, l2 := tr/2+disc, tr/2-disc
		if !(l2 > 0) {
			continue
		}
		ratios = append(ratios, math.Sqrt(l1/l2))
		th := math.Atan2(2*xy, xx-yy)
		w := math.Sqrt(l1/l2) - 1
		cx += w * math.Cos(th)
		cy += w * math.Sin(th)
	}
	if len(ratios) < shapeMinStars {
		return math.NaN(), math.NaN()
	}
	return median64(ratios), math.Mod(math.Atan2(cy, cx)/2/deg+180, 180)
}

func momentCentre(p *Plane, x, y, r float64) (mx, my float64, ok bool) {
	var s, sx, sy float64
	for yy := int(math.Floor(y - r)); yy <= int(math.Ceil(y+r)); yy++ {
		for xx := int(math.Floor(x - r)); xx <= int(math.Ceil(x+r)); xx++ {
			dx, dy := float64(xx)-x, float64(yy)-y
			if dx*dx+dy*dy > r*r || xx < 0 || yy < 0 || xx >= p.W || yy >= p.H {
				continue
			}
			v := float64(p.Data[yy*p.W+xx])
			if math.IsNaN(v) {
				return 0, 0, false
			}
			if v > 0 {
				s += v
				sx += v * float64(xx)
				sy += v * float64(yy)
			}
		}
	}
	if !(s > 0) {
		return 0, 0, false
	}
	return sx / s, sy / s, true
}

func moments(p *Plane, x, y, r float64) (xx, yy, xy float64, ok bool) {
	var s float64
	for py := int(math.Floor(y - r)); py <= int(math.Ceil(y+r)); py++ {
		for px := int(math.Floor(x - r)); px <= int(math.Ceil(x+r)); px++ {
			dx, dy := float64(px)-x, float64(py)-y
			if dx*dx+dy*dy > r*r || px < 0 || py < 0 || px >= p.W || py >= p.H {
				continue
			}
			v := float64(p.Data[py*p.W+px])
			if math.IsNaN(v) {
				return 0, 0, 0, false
			}
			if v > 0 {
				s += v
				xx += v * dx * dx
				yy += v * dy * dy
				xy += v * dx * dy
			}
		}
	}
	if !(s > 0) {
		return 0, 0, 0, false
	}
	return xx / s, yy / s, xy / s, true
}

// shapeRadius is the radius star shapes are measured within: four star
// sigmas, at least 4 px.
func shapeRadius(res *Result) float64 {
	return math.Max(4, 4*res.HFRNight/hfrToSigma)
}

// elongLimit is the axis ratio over the elongPercentile of a reference's
// subs, NaN with too few measured.
func elongLimit(ratios []float64) float64 {
	var v []float64
	for _, r := range ratios {
		if !math.IsNaN(r) {
			v = append(v, r)
		}
	}
	if len(v) < elongMinSubs {
		return math.NaN()
	}
	slices.Sort(v)
	return v[min(len(v)-1, int(math.Ceil(elongPercentile*float64(len(v))))-1)]
}

// elongated says why a change is the residual of elongated stars, or "":
// more than half the subs it is in, at least elongMinSubs, have stars
// longer than their filter's reference subs at elongPercentile, their
// median axis ratio is elongMinRatio or more, and the night's subs with
// round stars do not hold it at meanAgreeSigma. Every star leaves a dipole
// in the difference when the night's stars are longer than the
// reference's: candidate 255622 (Cygnis Loop Panel 1, R, G and B,
// 2026-09-02) sat in a lobe between two stars, its subs' stars at axis
// ratios 1.27 to 1.46 against 1.14 at the median of the other nights.
func (c *cand) elongated() string {
	if len(c.shape) != len(c.fluxes) || len(c.shapeLim) != len(c.fluxes) {
		return ""
	}
	var in, round []int
	var ratios, angles, lims []float64
	above := 0
	for k, f := range c.fluxes {
		r, lim := c.shape[k], c.shapeLim[k]
		if math.IsNaN(r) || math.IsNaN(lim) {
			continue
		}
		if r <= lim {
			round = append(round, k)
		}
		if !(f > presenceSigma*c.errs[k]) {
			continue
		}
		in = append(in, k)
		ratios, lims = append(ratios, r), append(lims, lim)
		if k < len(c.angle) {
			angles = append(angles, c.angle[k])
		}
		if r > lim {
			above++
		}
	}
	if len(in) < elongMinSubs || 2*above <= len(in) {
		return ""
	}
	med := median64(ratios)
	if med < elongMinRatio {
		return ""
	}
	if len(round) >= 2 {
		if st := c.pick(round).perSub(); st.meanSigma >= meanAgreeSigma {
			return ""
		}
	}
	return fmt.Sprintf("elongated stars: axis ratio %.2f at %.0f deg in %d of the %d subs it is in, over the reference's %.2f; not in the night's %d subs of round stars",
		med, median64(angles), above, len(in), median64(lims), len(round))
}

// pick is the candidate measured in its subs idx alone.
func (c *cand) pick(idx []int) *cand {
	o := &cand{Candidate: c.Candidate, wide: c.wide, refNights: c.refNights, refLight: c.refLight}
	o.Light = nil
	for _, k := range idx {
		if k >= len(c.fluxes) {
			continue
		}
		o.fluxes, o.errs = append(o.fluxes, c.fluxes[k]), append(o.errs, c.errs[k])
		if k < len(c.Light) {
			o.Light = append(o.Light, c.Light[k])
		}
		for _, s := range []struct {
			from []float64
			to   *[]float64
		}{{c.shared, &o.shared}, {c.expo, &o.expo}, {c.shape, &o.shape}, {c.angle, &o.angle}, {c.shapeLim, &o.shapeLim}, {c.sharp, &o.sharp}} {
			if k < len(s.from) {
				*s.to = append(*s.to, s.from[k])
			}
		}
		if k < len(c.sensor) {
			o.sensor = append(o.sensor, c.sensor[k])
		}
		if k < len(c.off) {
			o.off = append(o.off, c.off[k])
		}
	}
	return o
}
