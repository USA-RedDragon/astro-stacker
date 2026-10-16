package ownimage

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
)

const (
	defaultRefSubs = 48
	refApart       = 30 * 24 * time.Hour
	reprocessAfter = 48 * time.Hour
	matchArcsec    = 2.0
	matchWide      = 4.0
	matchMag       = 0.3
	compRadius     = 0.2
	compWide       = 0.4
	compMaxMag     = 1.0
	compMaxColor   = 0.5
	maxComps       = 15
	minComps       = 4
	compMargin     = 30.0
	compNeighbour  = 4.0
	compNbMag      = 1.5
	nbFaintest     = 20.8
	noGaiaG        = 16.0
	dateLayout     = "2006-01-02"
	shapeBrightest = 11.5
	shapeFaintest  = 15.0
	shapeLonely    = 8.0
	shapeNbMag     = 4.0
)

// refSet is the reference a comparison of one night stacked for one
// filter, as rebuilt from the subs.
type refSet struct {
	night  string
	filter string
	nights []int
	refs   []int
}

// spotPlan is what one spot needs from each sub.
type spotPlan struct {
	spot     Spot
	x, y     float64
	inFrame  bool
	star     *Star
	target   *starModel
	comps    []*starModel
	shape    []*starModel
	flagged  map[string]bool
	groups   map[string]string
	sets     []refSet
	night    map[int]bool
	ref      map[int]bool
	recs     []*rec
	fadeSign int
}

func nightKey(t time.Time) string { return t.UTC().Format(dateLayout) }

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// inRef says whether a sub could have been in a reference stacked at
// runAt. A sub reprocessed after the run keeps its frame but gets a new
// time, so only subs taken within two days of the run are judged by when
// they were added.
func inRef(s Sub, runAt time.Time) bool {
	if runAt.IsZero() {
		return true
	}
	if !s.Start.Before(runAt) {
		return false
	}
	if s.AddedAt.IsZero() || s.AddedAt.Before(runAt) {
		return true
	}
	return runAt.Sub(s.Start) > reprocessAfter
}

// pickRef takes up to n subs, the best of each night in turn, the nights
// at least 30 days away first and the farthest first, as the discoveries
// runner does.
func pickRef(subs []Sub, byNight map[time.Time][]int, night time.Time, n int) []int {
	away := func(t time.Time) time.Duration { return max(t.Sub(night), night.Sub(t)) }
	var far, near []time.Time
	for k, idx := range byNight {
		sort.SliceStable(idx, func(a, b int) bool { return subs[idx[a]].Weight > subs[idx[b]].Weight })
		if away(k) >= refApart {
			far = append(far, k)
		} else {
			near = append(near, k)
		}
	}
	for _, ks := range [][]time.Time{far, near} {
		sort.Slice(ks, func(a, b int) bool {
			if away(ks[a]) != away(ks[b]) {
				return away(ks[a]) > away(ks[b])
			}
			return ks[a].Before(ks[b])
		})
	}
	out := inTurn(byNight, far, n, nil)
	return inTurn(byNight, near, n, out)
}

func inTurn(byNight map[time.Time][]int, keys []time.Time, n int, out []int) []int {
	for i := 0; len(out) < n; i++ {
		added := false
		for _, k := range keys {
			if i < len(byNight[k]) && len(out) < n {
				out = append(out, byNight[k][i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

// refSets rebuilds the reference of every flagged night and filter.
func refSets(subs []Sub, sp *spotPlan, refSubs int) {
	sp.night, sp.ref = map[int]bool{}, map[int]bool{}
	for _, n := range sp.spot.Nights {
		nk := nightKey(n.Night)
		light := map[int]bool{}
		for _, l := range n.Light {
			light[l.FrameID] = true
		}
		byFilter := map[string][]int{}
		for i, s := range subs {
			if nightKey(s.Night) != nk || discoveries.GroupOf(s.Filter) != n.Group {
				continue
			}
			if len(light) > 0 && !light[s.FrameID] || len(light) == 0 && !n.RunAt.IsZero() && !s.Start.Before(n.RunAt) {
				continue
			}
			byFilter[s.Filter] = append(byFilter[s.Filter], i)
		}
		filters := make([]string, 0, len(byFilter))
		for f := range byFilter {
			filters = append(filters, f)
		}
		sort.Strings(filters)
		for _, f := range filters {
			others := map[time.Time][]int{}
			for i, s := range subs {
				if s.Filter != f || nightKey(s.Night) == nk || !inRef(s, n.RunAt) {
					continue
				}
				d := dateOnly(s.Night)
				others[d] = append(others[d], i)
			}
			set := refSet{night: nk, filter: f, nights: byFilter[f], refs: pickRef(subs, others, dateOnly(n.Night), refSubs)}
			for _, i := range set.nights {
				sp.night[i] = true
			}
			for _, i := range set.refs {
				sp.ref[i] = true
			}
			sp.sets = append(sp.sets, set)
		}
	}
}

// planSpot places the spot and picks its comparison stars.
func planSpot(ctx context.Context, d Data, w *discoveries.WCS, width, height int, subs []Sub, s Spot, refSubs int) (*spotPlan, error) {
	sp := &spotPlan{spot: s, flagged: map[string]bool{}, groups: map[string]string{}, recs: make([]*rec, len(subs)), fadeSign: 0}
	for _, n := range s.Nights {
		sp.flagged[nightKey(n.Night)] = true
		sp.groups[nightKey(n.Night)+"\x00"+n.Group] = n.Group
		if n.Sign != 0 && sp.fadeSign == 0 {
			sp.fadeSign = n.Sign
		}
	}
	x, y, ok := w.PixelOf(s.RA, s.Dec)
	if !ok || x < 0 || y < 0 || x >= float64(width) || y >= float64(height) {
		return sp, nil
	}
	sp.x, sp.y, sp.inFrame = x, y, true
	refSets(subs, sp, refSubs)
	stars, err := d.Stars(ctx, s.RA, s.Dec, compRadius+0.01)
	if err != nil {
		return nil, err
	}
	pickComps(sp, w, width, height, stars)
	if len(sp.comps) < minComps {
		wide, err := d.Stars(ctx, s.RA, s.Dec, compWide+0.01)
		if err != nil {
			return nil, err
		}
		pickComps(sp, w, width, height, wide)
	}
	return sp, nil
}

type placed struct {
	Star
	x, y float64
}

func pickComps(sp *spotPlan, w *discoveries.WCS, width, height int, stars []Star) {
	pl := make([]placed, 0, len(stars))
	pts := make([][2]float64, 0, len(stars))
	for _, s := range stars {
		x, y, ok := w.PixelOf(s.RA, s.Dec)
		if !ok || !finite(s.G) || s.G > nbFaintest {
			continue
		}
		pl = append(pl, placed{s, x, y})
		pts = append(pts, [2]float64{x, y})
	}
	grid := discoveries.NewGrid(pts, fitReach)
	model := func(x, y float64) *starModel {
		m := &starModel{x: x, y: y, self: -1}
		best := math.Inf(1)
		for _, i := range grid.Within(x, y, fitReach) {
			m.nbs = append(m.nbs, [2]float64{pl[i].x, pl[i].y})
			if d := math.Hypot(pl[i].x-x, pl[i].y-y); d < best {
				best, m.self = d, len(m.nbs)-1
			}
		}
		if m.self < 0 || best > 0.5 {
			m.nbs = append(m.nbs, [2]float64{x, y})
			m.self = len(m.nbs) - 1
		}
		return m
	}
	star := matchStar(pl, grid, sp, w.PixelScale())
	tg, tc := noGaiaG, math.NaN()
	if star != nil {
		s := star.Star
		sp.star = &s
		tg, tc = s.G, s.BPRP
		sp.target = model(star.x, star.y)
	} else {
		if sp.spot.GaiaG != nil {
			tg = *sp.spot.GaiaG
		}
		sp.target = model(sp.x, sp.y)
	}
	type cand struct {
		p placed
		d float64
	}
	var cands []cand
	for _, c := range pl {
		d := math.Hypot(c.x-sp.x, c.y-sp.y)
		if d >= compMargin && compOK(c, tg, tc, width, height) && !crowded(pl, grid, c) {
			cands = append(cands, cand{c, d})
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d < cands[b].d })
	if len(cands) > maxComps {
		cands = cands[:maxComps]
	}
	sp.comps = sp.comps[:0]
	for _, c := range cands {
		sp.comps = append(sp.comps, model(c.p.x, c.p.y))
	}
	sp.shape = shapeStars(pl, grid, sp, model)
	if len(sp.shape) == 0 {
		sort.SliceStable(cands, func(a, b int) bool { return cands[a].p.G < cands[b].p.G })
		for _, c := range cands[:min(shapeBright, len(cands))] {
			sp.shape = append(sp.shape, model(c.p.x, c.p.y))
		}
	}
}

// compOK says whether a star is like the target in brightness and color,
// not known to vary, and well inside the frame.
func compOK(c placed, g, color float64, width, height int) bool {
	if math.Abs(c.G-g) > compMaxMag || c.Variable {
		return false
	}
	if finite(color) && (!finite(c.BPRP) || math.Abs(c.BPRP-color) >= compMaxColor) {
		return false
	}
	return c.x >= compMargin && c.y >= compMargin && c.x <= float64(width)-compMargin && c.y <= float64(height)-compMargin
}

// crowded says whether a star has a neighbour close and bright enough to
// spoil it as a comparison.
func crowded(pl []placed, grid *discoveries.Grid, c placed) bool {
	for _, i := range grid.Within(c.x, c.y, compNeighbour) {
		o := pl[i]
		if (o.x != c.x || o.y != c.y) && o.G < c.G+compNbMag {
			return true
		}
	}
	return false
}

// matchStar is the Gaia star at the spot: the nearest within 2", or,
// when the detection named its star's magnitude, the nearest within 4"
// that matches it, as a spot found in a difference image can sit a pixel
// off the star it belongs to.
func matchStar(pl []placed, grid *discoveries.Grid, sp *spotPlan, scale float64) *placed {
	var star *placed
	best := math.Inf(1)
	for _, i := range grid.Within(sp.x, sp.y, matchWide/scale) {
		d := math.Hypot(pl[i].x-sp.x, pl[i].y-sp.y) * scale
		g := sp.spot.GaiaG
		ok := d <= matchArcsec
		if g != nil {
			ok = math.Abs(pl[i].G-*g) <= matchMag
		}
		if ok && d < best {
			best, star = d, &pl[i]
		}
	}
	return star
}

// shapeStars are the nearest bright, unsaturated, isolated stars, to
// measure each sub's star width and shift around the spot.
func shapeStars(pl []placed, grid *discoveries.Grid, sp *spotPlan, model func(x, y float64) *starModel) []*starModel {
	type cand struct {
		p placed
		d float64
	}
	var cands []cand
	for _, c := range pl {
		if c.G < shapeBrightest || c.G > shapeFaintest {
			continue
		}
		lonely := true
		for _, i := range grid.Within(c.x, c.y, shapeLonely) {
			o := pl[i]
			if (o.x != c.x || o.y != c.y) && o.G < c.G+shapeNbMag {
				lonely = false
				break
			}
		}
		if lonely {
			cands = append(cands, cand{c, math.Hypot(c.x-sp.x, c.y-sp.y)})
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d < cands[b].d })
	out := make([]*starModel, 0, min(shapeBright, len(cands)))
	for _, c := range cands[:min(shapeBright, len(cands))] {
		out = append(out, model(c.p.x, c.p.y))
	}
	return out
}
