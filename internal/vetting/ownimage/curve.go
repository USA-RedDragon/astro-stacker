package ownimage

import (
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/lc"
)

const (
	dropFactor   = 1.5
	maxDrops     = 3
	quietMaxErr  = 0.1
	minPresent   = 3
	minNightSubs = 2
	magPerFlux   = 1.0857362
)

// ensemble is the steady comparison stars kept for a spot and each sub's
// transparency against them.
type ensemble struct {
	kept    []int
	dropped int
	medians map[string][]float64
	transp  map[int]float64
}

func compMedians(subs []Sub, sp *spotPlan, n int) map[string][]float64 {
	vals := map[string][][]float64{}
	for i, r := range sp.recs {
		if r == nil || r.comps == nil {
			continue
		}
		f := subs[i].Filter
		if vals[f] == nil {
			vals[f] = make([][]float64, n)
		}
		for j, v := range r.comps {
			if v > 0 {
				vals[f][j] = append(vals[f][j], v)
			}
		}
	}
	out := map[string][]float64{}
	for f, vs := range vals {
		out[f] = make([]float64, n)
		for j := range vs {
			out[f][j] = median(vs[j])
		}
	}
	return out
}

// ratioTo is a sub's flux v against the kept comparison stars other than
// skip, scaled for any missing so the sum stays on one footing.
func ratioTo(r *rec, med []float64, kept []int, skip int) (float64, bool) {
	var sum, msum, mall float64
	present := 0
	for _, j := range kept {
		if j == skip || !(med[j] > 0) {
			continue
		}
		mall += med[j]
		if r.comps[j] > 0 {
			sum += r.comps[j]
			msum += med[j]
			present++
		}
	}
	if present < minPresent || !(msum > 0) || !(sum > 0) {
		return 0, false
	}
	return sum * mall / msum, true
}

func leaveOneOut(subs []Sub, sp *spotPlan, med map[string][]float64, kept []int) map[int]float64 {
	out := map[int]float64{}
	for _, j := range kept {
		byFilter := map[string][]float64{}
		for i, r := range sp.recs {
			if r == nil || r.comps == nil || !(r.comps[j] > 0) {
				continue
			}
			s, ok := ratioTo(r, med[subs[i].Filter], kept, j)
			if !ok {
				continue
			}
			byFilter[subs[i].Filter] = append(byFilter[subs[i].Filter], -2.5*math.Log10(r.comps[j]/s))
		}
		var res []float64
		for _, v := range byFilter {
			m := median(v)
			for _, x := range v {
				res = append(res, x-m)
			}
		}
		if len(res) >= minPresent {
			out[j] = robustStd(res)
		}
	}
	return out
}

// buildEnsemble keeps the comparison stars whose leave-one-out scatter is
// within 1.5 times the median, dropping the worst one at a time.
func buildEnsemble(subs []Sub, sp *spotPlan) *ensemble {
	e := &ensemble{medians: compMedians(subs, sp, len(sp.comps)), transp: map[int]float64{}}
	for j := range sp.comps {
		e.kept = append(e.kept, j)
	}
	for range maxDrops {
		if len(e.kept) <= minComps {
			break
		}
		sd := leaveOneOut(subs, sp, e.medians, e.kept)
		all := make([]float64, 0, len(sd))
		for _, v := range sd {
			all = append(all, v)
		}
		lim := dropFactor * median(all)
		worst, wv := -1, lim
		for _, j := range e.kept {
			v, ok := sd[j]
			if !ok {
				v = math.Inf(1)
			}
			if v > wv {
				worst, wv = j, v
			}
		}
		if worst < 0 {
			break
		}
		e.kept = removeInt(e.kept, worst)
		e.dropped++
	}
	for i, r := range sp.recs {
		if r == nil || r.comps == nil {
			continue
		}
		med := e.medians[subs[i].Filter]
		s, ok := ratioTo(r, med, e.kept, -1)
		if !ok {
			continue
		}
		var mall float64
		for _, j := range e.kept {
			if med[j] > 0 {
				mall += med[j]
			}
		}
		e.transp[i] = s / mall
	}
	return e
}

func removeInt(v []int, x int) []int {
	out := v[:0:0]
	for _, y := range v {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

func midTime(s Sub) time.Time {
	return s.Start.Add(time.Duration(s.Exposure / 2 * float64(time.Second)))
}

// buildCurve measures the spot's own light curve against the ensemble,
// each filter set to its median on nights not flagged. A filter taken
// only on flagged nights is set, night by night, to its median on the
// other flagged nights, as the detection's reference was built from the
// filter's subs of other nights.
func buildCurve(subs []Sub, sp *spotPlan, e *ensemble) *Curve {
	c := &Curve{Comps: len(e.kept), Dropped: e.dropped}
	raw := map[int]float64{}
	normal := map[string][]float64{}
	flagged := map[string]map[string][]float64{}
	for i, r := range sp.recs {
		t, ok := e.transp[i]
		if r == nil || !ok || !r.fluxOK || !(r.flux > 0) {
			continue
		}
		raw[i] = -2.5 * math.Log10(r.flux/t)
		f, nk := subs[i].Filter, nightKey(subs[i].Night)
		if !sp.flagged[nk] {
			normal[f] = append(normal[f], raw[i])
			continue
		}
		if flagged[f] == nil {
			flagged[f] = map[string][]float64{}
		}
		flagged[f][nk] = append(flagged[f][nk], raw[i])
	}
	zero := map[string]float64{}
	for f, v := range normal {
		zero[f] = median(v)
	}
	zeroOf := func(f, night string) (float64, bool) {
		if z, ok := zero[f]; ok {
			return z, true
		}
		var v []float64
		for k, vs := range flagged[f] {
			if k != night {
				v = append(v, vs...)
			}
		}
		if len(v) == 0 {
			return 0, false
		}
		return median(v), true
	}
	for i, m := range raw {
		s := subs[i]
		z, ok := zeroOf(s.Filter, nightKey(s.Night))
		if !ok {
			continue
		}
		r := sp.recs[i]
		c.Points = append(c.Points, Point{FrameID: s.FrameID, BJD: lc.BJD(midTime(s), sp.spot.RA, sp.spot.Dec), Night: nightKey(s.Night),
			Filter: s.Filter, DMag: m - z, Err: magPerFlux * r.ferr / r.flux})
	}
	sort.Slice(c.Points, func(a, b int) bool { return c.Points[a].BJD < c.Points[b].BJD })
	c.Nights = nightLevels(c.Points)
	floorErrors(c, sp)
	return c
}

// floorErrors keeps each night's error no smaller than its filter's single
// frame scatter on normal nights over the root of its frame count, as a
// few frames can agree by chance.
func floorErrors(c *Curve, sp *spotPlan) {
	type key struct{ night, filter string }
	level := map[key]float64{}
	for _, n := range c.Nights {
		level[key{n.Night, n.Filter}] = n.DMag
	}
	res := map[string][]float64{}
	for _, p := range c.Points {
		if !sp.flagged[p.Night] {
			res[p.Filter] = append(res[p.Filter], p.DMag-level[key{p.Night, p.Filter}])
		}
	}
	for i, n := range c.Nights {
		v := res[n.Filter]
		if len(v) < minPresent {
			continue
		}
		dof := float64(len(v)) / float64(max(len(v)-countNightsOf(c, n.Filter, sp), 1))
		floor := robustStd(v) * math.Sqrt(dof) / math.Sqrt(float64(n.N))
		if floor > n.Err {
			c.Nights[i].Err = floor
		}
	}
}

func countNightsOf(c *Curve, filter string, sp *spotPlan) int {
	k := 0
	for _, n := range c.Nights {
		if n.Filter == filter && !sp.flagged[n.Night] {
			k++
		}
	}
	return k
}

func nightLevels(pts []Point) []NightLevel {
	type key struct{ night, filter string }
	groups := map[key][]Point{}
	var keys []key
	for _, p := range pts {
		k := key{p.Night, p.Filter}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], p)
	}
	out := make([]NightLevel, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		v := make([]float64, len(g))
		errs := make([]float64, len(g))
		for i, p := range g {
			v[i], errs[i] = p.DMag, p.Err
		}
		m, sd, n := clippedMean(v)
		e := median(errs) / math.Sqrt(float64(n))
		if n >= 3 && sd/math.Sqrt(float64(n)) > e {
			e = sd / math.Sqrt(float64(n))
		}
		out = append(out, NightLevel{Night: k.night, Filter: k.filter, N: len(g), DMag: m, Err: e})
	}
	return out
}

// summarize fills the curve's scatter, change and quiet fields.
func summarize(c *Curve, sp *spotPlan) {
	levels := map[string]float64{}
	for _, n := range c.Nights {
		levels[n.Night+"\x00"+n.Filter] = n.DMag
	}
	var res, normal []float64
	for _, p := range c.Points {
		if sp.flagged[p.Night] || !sp.groupFilter(p.Filter) {
			continue
		}
		normal = append(normal, p.DMag)
		for _, n := range c.Nights {
			if n.Night == p.Night && n.Filter == p.Filter && n.N >= 3 {
				res = append(res, p.DMag-n.DMag)
			}
		}
	}
	c.Scatter = robustStd(res)
	if !finite(c.Scatter) {
		c.Scatter = robustStd(normal)
	}
	var quietLv []float64
	for _, n := range c.Nights {
		if quietNight(sp, n) {
			quietLv = append(quietLv, n.DMag)
			if math.Abs(n.DMag) > math.Abs(c.Quiet) {
				c.Quiet = n.DMag
			}
		}
	}
	nightSD := robustStd(quietLv)
	if !finite(nightSD) {
		nightSD = 0
	}
	best := math.Inf(-1)
	var sw, swx float64
	for _, n := range c.Nights {
		if !sp.flagged[n.Night] || !sp.inGroup(n.Night, n.Filter) {
			continue
		}
		e2 := n.Err*n.Err + nightSD*nightSD
		sig := n.DMag / math.Sqrt(e2)
		score := math.Abs(sig)
		agree := n.DMag
		if s := sp.signOf(n.Night, n.Filter); s != 0 {
			score = -float64(s) * sig
			agree = float64(s*sp.fadeSign) * n.DMag
		}
		sw, swx = sw+1/e2, swx+agree/e2
		if score > best {
			best = score
			c.ChangeNight, c.Change, c.ChangeSigma = n.Night, n.DMag, sig
		}
	}
	if c.ChangeLevels = flaggedLevels(c, sp); c.ChangeLevels > 1 && sw > 0 {
		c.Change, c.ChangeSigma = swx/sw, swx/math.Sqrt(sw)
	}
}

func flaggedLevels(c *Curve, sp *spotPlan) int {
	k := 0
	for _, n := range c.Nights {
		if sp.flagged[n.Night] && sp.inGroup(n.Night, n.Filter) {
			k++
		}
	}
	return k
}

// groupFilter says whether a filter is in any group the spot was flagged
// in.
// signOf is the sign the detection gave a flagged night and filter: -1
// fainter, 1 brighter, or the spot's first sign when it gave none.
func (sp *spotPlan) signOf(night, filter string) int {
	for _, n := range sp.spot.Nights {
		if nightKey(n.Night) == night && discoveries.GroupOf(filter) == n.Group && n.Sign != 0 {
			return n.Sign
		}
	}
	return sp.fadeSign
}

func (sp *spotPlan) groupFilter(filter string) bool {
	for _, n := range sp.spot.Nights {
		if discoveries.GroupOf(filter) == n.Group {
			return true
		}
	}
	return false
}

// quietNight says whether a night level counts toward the other nights:
// not flagged, with two frames or more and measured to 0.1 mag.
func quietNight(sp *spotPlan, n NightLevel) bool {
	return !sp.flagged[n.Night] && n.N >= minNightSubs && n.Err <= quietMaxErr
}

func (sp *spotPlan) inGroup(night, filter string) bool {
	for _, n := range sp.spot.Nights {
		if nightKey(n.Night) == night && discoveries.GroupOf(filter) == n.Group {
			return true
		}
	}
	return false
}

func seriesOf(c *Curve) []lc.Series {
	if c == nil {
		return nil
	}
	byFilter := map[string][]lc.Point{}
	var order []string
	for _, p := range c.Points {
		if _, ok := byFilter[p.Filter]; !ok {
			order = append(order, p.Filter)
		}
		byFilter[p.Filter] = append(byFilter[p.Filter], lc.Point{BJD: p.BJD, Mag: p.DMag, Err: p.Err})
	}
	out := make([]lc.Series, 0, len(order))
	for _, f := range order {
		out = append(out, lc.Series{Survey: lc.Own, Band: f, Points: byFilter[f], Relative: true, Dilution: 1, Deblended: true})
	}
	return out
}
