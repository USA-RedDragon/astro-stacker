package period

import (
	"math"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

type shiftFit struct {
	tau, tauErr     float64
	scale, scaleErr float64
	offset          float64
	n               int
}

type linFit struct{ s, c, chi, smm float64 }

func solveScale(pts []obs, prof func(p obs, tau float64) float64, tau float64, withOffset bool) (linFit, bool) {
	var sw, sm, sy, smm, smy, syy float64
	for _, p := range pts {
		w := 1 / (p.e * p.e)
		m := prof(p, tau)
		sw += w
		sm += w * m
		sy += w * p.y
		smm += w * m * m
		smy += w * m * p.y
		syy += w * p.y * p.y
	}
	if !withOffset {
		if smm <= 0 {
			return linFit{chi: syy}, false
		}
		s := smy / smm
		return linFit{s: s, chi: syy - s*smy, smm: smm}, true
	}
	det := smm*sw - sm*sm
	if det <= 0 {
		return linFit{chi: syy}, false
	}
	s := (smy*sw - sm*sy) / det
	c := (smm*sy - sm*smy) / det
	chi := syy - s*smy - c*sy
	return linFit{s: s, c: c, chi: chi, smm: det / sw}, true
}

func fitShift(pts []obs, prof func(p obs, tau float64) float64, maxShift float64, withOffset bool) (shiftFit, bool) {
	np := 2
	if withOffset {
		np = 3
	}
	if len(pts) < np+1 || maxShift <= 0 {
		return shiftFit{}, false
	}
	steps := 800
	h := 2 * maxShift / float64(steps)
	fits := make([]linFit, steps+1)
	best := -1
	for j := range fits {
		lf, ok := solveScale(pts, prof, -maxShift+float64(j)*h, withOffset)
		fits[j] = lf
		if ok && (best < 0 || lf.chi < fits[best].chi) {
			best = j
		}
	}
	if best < 0 || fits[best].s <= 0 {
		return shiftFit{}, false
	}
	bf := fits[best]
	red := math.Max(1, bf.chi/float64(len(pts)-np))
	lim := bf.chi + red
	l, r := best, best
	for l > 0 && fits[l].chi < lim {
		l--
	}
	for r < steps && fits[r].chi < lim {
		r++
	}
	tau := -maxShift + float64(best)*h
	errTau := math.Max(float64(r-l)*h/2, h/2)
	if best > 0 && best < steps {
		c0, c1, c2 := fits[best-1].chi, bf.chi, fits[best+1].chi
		if den := c0 - 2*c1 + c2; den > 0 {
			tau += h * 0.5 * (c0 - c2) / den
		}
	}
	return shiftFit{tau: tau, tauErr: errTau, scale: bf.s, scaleErr: math.Sqrt(red / bf.smm), offset: bf.c, n: len(pts)}, true
}

func (e *Ephemeris) depthFor(k int, band string) float64 {
	if k >= len(e.Eclipses) {
		return 0
	}
	dep := e.Eclipses[k].Depth
	if v, ok := dep[band]; ok {
		return v
	}
	return meanOf(dep)
}

func (e *Ephemeris) minTime(k int, cycle float64) float64 {
	ph := 0.0
	if k < len(e.Eclipses) {
		ph = e.Eclipses[k].Phase
	}
	return e.T0 + (cycle+ph)*e.Period
}

func timeMinimum(e *Ephemeris, points []lc.Point, band string) (Timing, bool) {
	if e == nil || e.Period <= 0 || e.Kind != KindEclipsing || len(points) == 0 {
		return Timing{}, false
	}
	var mid float64
	for _, p := range points {
		mid += p.BJD
	}
	mid /= float64(len(points))
	bestK, bestN := -1, 0
	var bestPts []obs
	for k, ec := range e.Eclipses {
		half := ec.Width * e.Period
		cyc := math.Round((mid-e.T0)/e.Period - ec.Phase)
		for _, c := range []float64{cyc - 1, cyc, cyc + 1} {
			tm := e.minTime(k, c)
			var pts []obs
			for _, p := range points {
				if math.Abs(p.BJD-tm) < 1.5*half {
					er := p.Err
					if !(er > 0) {
						er = 0.01
					}
					pts = append(pts, obs{t: p.BJD - tm, y: p.Mag, e: math.Hypot(er, 0.002)})
				}
			}
			if len(pts) > bestN {
				bestK, bestN, bestPts = k, len(pts), pts
			}
		}
	}
	if bestK < 0 || bestN < 5 {
		return Timing{}, false
	}
	ec := e.Eclipses[bestK]
	half := ec.Width * e.Period
	depth := e.depthFor(bestK, band)
	prof := func(p obs, tau float64) float64 {
		sh := shapeOnly((p.t-tau)/e.Period, ec.Width, ec.Shape)
		return depth * sh
	}
	errs := make([]float64, len(bestPts))
	for i, p := range bestPts {
		errs[i] = p.e
	}
	equal := median(errs)
	for i := range bestPts {
		bestPts[i].e = equal
	}
	sf, ok := fitShift(bestPts, prof, 0.5*half, false)
	if !ok || sf.scale < 3*sf.scaleErr {
		return Timing{}, false
	}
	first, last := bestPts[0].t, bestPts[0].t
	core := false
	for _, p := range bestPts {
		first, last = math.Min(first, p.t), math.Max(last, p.t)
		if math.Abs(p.t-sf.tau) < 0.35*half {
			core = true
		}
	}
	if !core || sf.tau < first-0.1*half || sf.tau > last+0.1*half {
		return Timing{}, false
	}
	return Timing{
		Offset:    sf.tau * 1440,
		OffsetErr: sf.tauErr * 1440,
		Depth:     sf.scale * depth,
		Dip:       bestK + 1,
		Points:    sf.n,
		Scale:     sf.scale,
	}, true
}
