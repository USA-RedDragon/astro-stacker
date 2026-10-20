package period

import (
	"math"
	"slices"
	"strings"

	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

// Thresholds of the season to season test; set on source 167, whose ZTF
// r seasons go 16.22, 16.42, 16.24 while its comparison stars stay
// within 0.01 mag.
const (
	trendMinSeasonPts = 20
	trendMinSeasons   = 3
	trendMinRange     = 0.05
	trendMinZ         = 8.0
	trendCompRatio    = 4.0
	trendCompMargin   = 0.04
	trendMinComps     = 2
	trendMinBands     = 2
	trendErrFloor     = 0.003
)

// Trend is how the star's level changes from season to season in ZTF,
// band by band, against nearby stars of the same brightness.
type Trend struct {
	Bands []TrendBand `json:"bands"`
}

// TrendBand is one ZTF band's season to season change: Range is the
// faintest season's median less the brightest's, in magnitudes; Z is
// Range over its error; CompRange is the comparison stars' median Range
// and Comparisons how many were measured. Seasons are the season medians
// in time order, each as its mean BJD and magnitude offset.
type TrendBand struct {
	Band        string        `json:"band"`
	Range       float64       `json:"range"`
	Z           float64       `json:"z"`
	CompRange   float64       `json:"comp_range"`
	Comparisons int           `json:"comparisons"`
	Seasons     []TrendSeason `json:"seasons"`
}

// TrendSeason is one season's median level.
type TrendSeason struct {
	BJD float64 `json:"bjd"`
	Mag float64 `json:"mag"`
	N   int     `json:"n"`
}

// Proven says the season to season change shows in trendMinBands ZTF
// bands or more, beyond the comparison stars in each.
func (t *Trend) Proven() bool { return t != nil && len(t.Bands) >= trendMinBands }

// Largest is the band with the largest change.
func (t *Trend) Largest() TrendBand {
	var out TrendBand
	if t == nil {
		return out
	}
	for _, b := range t.Bands {
		if b.Range > out.Range {
			out = b
		}
	}
	return out
}

type seasonStat struct {
	rng, z float64
	ss     []TrendSeason
}

func seasonLevels(p []pt) (seasonStat, bool) {
	var ss []TrendSeason
	var errs []float64
	for _, s := range seasonsOf(p) {
		if len(s) < trendMinSeasonPts {
			continue
		}
		ys := make([]float64, len(s))
		t := 0.0
		for i, q := range s {
			ys[i] = q.y
			t += q.t
		}
		m := median(ys)
		e := math.Max(1.2533*mad(ys, m)/math.Sqrt(float64(len(ys))), trendErrFloor)
		ss = append(ss, TrendSeason{BJD: t / float64(len(s)), Mag: m, N: len(s)})
		errs = append(errs, e)
	}
	if len(ss) < trendMinSeasons {
		return seasonStat{}, false
	}
	lo, hi := 0, 0
	for i, s := range ss {
		if s.Mag < ss[lo].Mag {
			lo = i
		}
		if s.Mag > ss[hi].Mag {
			hi = i
		}
	}
	rng := ss[hi].Mag - ss[lo].Mag
	return seasonStat{rng: rng, z: rng / math.Hypot(errs[lo], errs[hi]), ss: ss}, true
}

// trend tests each ZTF band for a change from season to season that its
// comparison stars lack. The comparison stars are asked for only when
// the star itself changes.
func (a *analysis) trend() *Trend {
	type cand struct {
		key string
		st  seasonStat
	}
	var cs []cand
	for _, b := range a.bands() {
		if a.d.groups[b.g].survey != lc.ZTF {
			continue
		}
		st, ok := seasonLevels(b.raw)
		if !ok || st.rng < trendMinRange || st.z < trendMinZ {
			continue
		}
		cs = append(cs, cand{b.key, st})
	}
	if len(cs) < trendMinBands || a.opts.Comparisons == nil {
		return nil
	}
	cb := compBands(a.opts.Comparisons())
	t := &Trend{}
	for _, c := range cs {
		var rs []float64
		for _, m := range cb {
			if p, ok := m[c.key]; ok {
				if st, ok := seasonLevels(p); ok {
					rs = append(rs, st.rng)
				}
			}
		}
		if len(rs) < trendMinComps {
			continue
		}
		cm := median(rs)
		loud := 0
		for _, r := range rs {
			if r >= c.st.rng/2 {
				loud++
			}
		}
		if c.st.rng < trendCompRatio*cm || c.st.rng < cm+trendCompMargin || loud > compMaxSig {
			continue
		}
		t.Bands = append(t.Bands, TrendBand{Band: c.key, Range: c.st.rng, Z: c.st.z, CompRange: cm, Comparisons: len(rs), Seasons: c.st.ss})
	}
	if len(t.Bands) == 0 {
		return nil
	}
	slices.SortFunc(t.Bands, func(x, y TrendBand) int { return strings.Compare(x.Band, y.Band) })
	return t
}
