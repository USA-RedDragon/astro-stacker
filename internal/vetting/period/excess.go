package period

import (
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Thresholds of the excess scatter test; set on source 35413, whose ZTF
// g scatters 0.043 mag beyond its errors while four of its five
// comparison stars stay within 0.01 mag, and which ZTF has 0.12 mag
// brighter in g around the nights the user's frames flagged it brighter.
const (
	excessMin        = 0.02
	excessCompRatio  = 2.5
	excessCompMargin = 0.015
	excessMinComps   = 3
	excessMinBands   = 2
	flagMinPts       = 3
	flagMinOffset    = 0.05
	flagMinZ         = 3.0
)

// FlagWindow is how many days either side of a flagged night count as
// that night's ZTF level.
const FlagWindow = 10.0

// Excess is how much more the star scatters in ZTF than its errors say,
// band by band, against nearby stars of the same brightness, and its ZTF
// level around the user's flagged nights. Toward is the way the user's
// frames saw it change: -1 brighter, 1 fainter, 0 unknown.
type Excess struct {
	Bands  []ExcessBand `json:"bands"`
	Toward int          `json:"toward"`
}

// ExcessBand is one ZTF band's excess scatter: Excess is the robust
// scatter beyond the median error, in magnitudes; CompExcess is the
// comparison stars' median of it, Comparisons how many were measured and
// Steady how many scatter less than half the star's excess. Flagged is
// the star's median within FlagWindow days of a flagged night less its
// usual level, less the comparison stars' median of the same, from
// FlaggedPoints points; FlaggedZ is its size over its error.
type ExcessBand struct {
	Band          string  `json:"band"`
	Excess        float64 `json:"excess"`
	CompExcess    float64 `json:"comp_excess"`
	Comparisons   int     `json:"comparisons"`
	Steady        int     `json:"steady"`
	Flagged       float64 `json:"flagged"`
	FlaggedPoints int     `json:"flagged_points"`
	FlaggedZ      float64 `json:"flagged_z"`
}

// Proven says the star scatters beyond its comparison stars in
// excessMinBands ZTF bands or more.
func (x *Excess) Proven() bool { return x != nil && len(x.Bands) >= excessMinBands }

// Shown says the band's level around the flagged nights stands out.
func (b ExcessBand) Shown() bool {
	return b.FlaggedPoints >= flagMinPts && math.Abs(b.Flagged) >= flagMinOffset && b.FlaggedZ >= flagMinZ
}

// Agrees says ZTF has the star changed around the flagged nights the
// way the user's frames do in one band or more, and the other way in
// none.
func (x *Excess) Agrees() bool {
	if x == nil || x.Toward == 0 {
		return false
	}
	agree := false
	for _, b := range x.Bands {
		if !b.Shown() {
			continue
		}
		if (b.Flagged < 0) != (x.Toward < 0) {
			return false
		}
		agree = true
	}
	return agree
}

// Holds says the star scatters beyond its comparison stars and ZTF
// confirms the user's change on the flagged nights.
func (x *Excess) Holds() bool { return x.Proven() && x.Agrees() }

// Largest is the band whose flagged night level stands out most, or the
// one with the largest excess when none does.
func (x *Excess) Largest() ExcessBand {
	var out ExcessBand
	if x == nil {
		return out
	}
	for _, b := range x.Bands {
		switch {
		case b.Shown() && (!out.Shown() || math.Abs(b.Flagged) > math.Abs(out.Flagged)):
			out = b
		case !out.Shown() && !b.Shown() && b.Excess > out.Excess:
			out = b
		}
	}
	return out
}

type excessStat struct {
	ex, lvl, sig float64
}

func excessOf(p []pt) excessStat {
	ys := make([]float64, len(p))
	es := make([]float64, len(p))
	for i, q := range p {
		ys[i], es[i] = q.y, q.e
	}
	m := median(ys)
	s := mad(ys, m)
	e := median(es)
	return excessStat{ex: math.Sqrt(math.Max(s*s-e*e, 0)), lvl: m, sig: s}
}

func flaggedOffset(p []pt, lvl float64, nights []float64) (float64, int) {
	var ys []float64
	for _, q := range p {
		for _, n := range nights {
			if math.Abs(q.t-n) <= FlagWindow {
				ys = append(ys, q.y)
				break
			}
		}
	}
	if len(ys) == 0 {
		return 0, 0
	}
	return median(ys) - lvl, len(ys)
}

// excess tests each ZTF band for scatter beyond its errors that its
// comparison stars lack. The comparison stars are asked for only when
// the star itself scatters beyond excessMin in excessMinBands bands.
func (a *analysis) excess() *Excess {
	type cand struct {
		key string
		raw []pt
		st  excessStat
	}
	var cs []cand
	for _, b := range a.bands() {
		if a.d.groups[b.g].survey != lc.ZTF {
			continue
		}
		if st := excessOf(b.raw); st.ex >= excessMin {
			cs = append(cs, cand{b.key, b.raw, st})
		}
	}
	if len(cs) < excessMinBands || a.opts.Comparisons == nil {
		return nil
	}
	cb := compBands(a.opts.Comparisons())
	x := &Excess{}
	switch {
	case a.opts.ChangeMag < 0:
		x.Toward = -1
	case a.opts.ChangeMag > 0:
		x.Toward = 1
	}
	for _, c := range cs {
		var exs, offs []float64
		for _, m := range cb {
			p, ok := m[c.key]
			if !ok {
				continue
			}
			st := excessOf(p)
			exs = append(exs, st.ex)
			if off, n := flaggedOffset(p, st.lvl, a.opts.Flagged); n >= flagMinPts {
				offs = append(offs, off)
			}
		}
		if len(exs) < excessMinComps {
			continue
		}
		cm := median(exs)
		steady := 0
		for _, e := range exs {
			if e < c.st.ex/2 {
				steady++
			}
		}
		if c.st.ex < excessCompRatio*cm || c.st.ex < cm+excessCompMargin || len(exs)-steady > compMaxSig {
			continue
		}
		xb := ExcessBand{Band: c.key, Excess: c.st.ex, CompExcess: cm, Comparisons: len(exs), Steady: steady}
		if off, n := flaggedOffset(c.raw, c.st.lvl, a.opts.Flagged); n > 0 {
			if len(offs) > 0 {
				off -= median(offs)
			}
			xb.Flagged, xb.FlaggedPoints = off, n
			xb.FlaggedZ = math.Abs(off) / (1.2533 * c.st.sig / math.Sqrt(float64(n)))
		}
		x.Bands = append(x.Bands, xb)
	}
	if len(x.Bands) == 0 {
		return nil
	}
	slices.SortFunc(x.Bands, func(p, q ExcessBand) int { return strings.Compare(p.Band, q.Band) })
	return x
}
