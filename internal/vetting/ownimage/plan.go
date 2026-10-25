package ownimage

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
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
	nbDepth        = 5.0
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
	tile     *tile
	flagged  map[string]bool
	groups   map[string]string
	sets     []refSet
	night    map[int]bool
	ref      map[int]bool
	recs     []*rec
	stamps   *stamps
	fadeSign int
	// mirrors are the filters in which two flagged nights are each
	// other's only reference.
	mirrors []Mirror
	// masters are each filter's master around a spot with no star, and
	// mSigma their star widths.
	masters map[string][]float32
	mSigma  map[string]float64
	// bare is the work only a spot with no star needs: the places around
	// it measured the same way, the star whose wing may reach it, and how
	// clear it is of bright stars and the edge.
	bare *barePlan
	// scale is the frame's pixel scale in arcsec.
	scale float64
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
	sp.mirrors = mirrorsOf(subs, sp.sets)
}

// field is a target's Gaia stars, read once, placed on its frame, with
// one model per star shared by every spot that fits it.
type field struct {
	w             *discoveries.WCS
	width, height int
	pl            []placed
	grid          *discoveries.Grid
	models        map[int]*starModel
	tile          *tile
}

// tile is the stars that set one star width and shift per sub for every
// spot of the target.
type tile struct {
	shape []*starModel
}

// newField reads the Gaia stars around every spot in one cone.
func newField(ctx context.Context, d Data, w *discoveries.WCS, width, height int, spots []*spotPlan) (*field, error) {
	f := &field{w: w, width: width, height: height, models: map[int]*starModel{}}
	var in []*spotPlan
	for _, sp := range spots {
		if sp.inFrame {
			in = append(in, sp)
		}
	}
	if len(in) == 0 {
		return f, nil
	}
	var vx, vy, vz float64
	for _, sp := range in {
		r, d := sp.spot.RA*math.Pi/180, sp.spot.Dec*math.Pi/180
		vx, vy, vz = vx+math.Cos(d)*math.Cos(r), vy+math.Cos(d)*math.Sin(r), vz+math.Sin(d)
	}
	ra := math.Mod(math.Atan2(vy, vx)*180/math.Pi+360, 360)
	dec := math.Atan2(vz, math.Hypot(vx, vy)) * 180 / math.Pi
	var reach float64
	for _, sp := range in {
		reach = math.Max(reach, discoveries.Separation(ra, dec, sp.spot.RA, sp.spot.Dec))
	}
	stars, err := d.Stars(ctx, ra, dec, reach+compWide+0.01)
	if err != nil {
		return nil, err
	}
	pts := make([][2]float64, 0, len(stars))
	for _, s := range stars {
		x, y, ok := w.PixelOf(s.RA, s.Dec)
		if !ok || !finite(s.G) || s.G > nbFaintest {
			continue
		}
		near := false
		for _, sp := range in {
			if discoveries.Separation(s.RA, s.Dec, sp.spot.RA, sp.spot.Dec) <= compWide+0.01 {
				near = true
				break
			}
		}
		if near {
			f.pl = append(f.pl, placed{s, x, y})
			pts = append(pts, [2]float64{x, y})
		}
	}
	f.grid = discoveries.NewGrid(pts, fitReach)
	return f, nil
}

// model is the shared fit model of placed star i.
func (f *field) model(i int) *starModel {
	if m, ok := f.models[i]; ok {
		return m
	}
	m := f.modelAt(f.pl[i].x, f.pl[i].y, f.pl[i].G+nbDepth)
	m.g = f.pl[i].G
	f.models[i] = m
	return m
}

// modelAt models a star at (x, y) with its neighbours down to faintest;
// fainter ones change its fit by under 1%.
func (f *field) modelAt(x, y, faintest float64) *starModel {
	m := &starModel{x: x, y: y, self: -1}
	best := math.Inf(1)
	for _, i := range f.grid.Within(x, y, fitReach) {
		if f.pl[i].G > faintest && math.Hypot(f.pl[i].x-x, f.pl[i].y-y) > 0.5 {
			continue
		}
		m.nbs = append(m.nbs, [2]float64{f.pl[i].x, f.pl[i].y})
		m.gs = append(m.gs, f.pl[i].G)
		if d := math.Hypot(f.pl[i].x-x, f.pl[i].y-y); d < best {
			best, m.self = d, len(m.nbs)-1
		}
	}
	if m.self < 0 || best > 0.5 {
		m.nbs = append(m.nbs, [2]float64{x, y})
		m.gs = append(m.gs, math.NaN())
		m.self = len(m.nbs) - 1
	}
	return m
}

// newSpotPlan places the spot and rebuilds its references.
func newSpotPlan(w *discoveries.WCS, width, height int, subs []Sub, s Spot, refSubs int) *spotPlan {
	sp := &spotPlan{spot: s, flagged: map[string]bool{}, groups: map[string]string{}}
	for _, n := range s.Nights {
		sp.flagged[nightKey(n.Night)] = true
		sp.groups[nightKey(n.Night)+"\x00"+n.Group] = n.Group
		if n.Sign != 0 && sp.fadeSign == 0 {
			sp.fadeSign = n.Sign
		}
	}
	if s.Kind == app.DiscoveryKindMover {
		return sp
	}
	x, y, ok := w.PixelOf(s.RA, s.Dec)
	if !ok || x < 0 || y < 0 || x >= float64(width) || y >= float64(height) {
		return sp
	}
	sp.x, sp.y, sp.inFrame, sp.scale = x, y, true, w.PixelScale()
	sp.recs = make([]*rec, len(subs))
	refSets(subs, sp, refSubs)
	return sp
}

type placed struct {
	Star
	x, y float64
}

// pickComps matches the spot's star and picks its comparison stars.
func (f *field) pickComps(sp *spotPlan) {
	pl, grid := f.pl, f.grid
	star := matchStar(pl, grid, sp, f.w.PixelScale())
	tg, tc := noGaiaG, math.NaN()
	if star >= 0 {
		s := pl[star].Star
		sp.star = &s
		tg, tc = s.G, s.BPRP
		sp.target = f.model(star)
	} else {
		if sp.spot.GaiaG != nil {
			tg = *sp.spot.GaiaG
		}
		sp.target = f.modelAt(sp.x, sp.y, nbFaintest)
	}
	for _, radius := range []float64{compRadius, compWide} {
		sp.comps = f.comps(sp, tg, tc, radius)
		if len(sp.comps) >= minComps {
			break
		}
	}
	if f.tile == nil {
		f.tile = &tile{}
	}
	sp.tile = f.tile
}

// shapes picks each tile's width stars near the middle of its spots.
// shapes picks the stars that set each sub's star width and shift, once
// for every spot: the nearest bright, isolated stars to each spot in turn.
func (f *field) shapes(spots []*spotPlan) {
	if f.tile == nil {
		return
	}
	var in []*spotPlan
	for _, sp := range spots {
		if sp.inFrame {
			in = append(in, sp)
		}
	}
	per := max(1, shapeBright/len(in))
	seen := map[*starModel]bool{}
	for _, sp := range in {
		for _, m := range f.shapeStars(sp.x, sp.y, per) {
			if !seen[m] && len(f.tile.shape) < shapeBright {
				seen[m] = true
				f.tile.shape = append(f.tile.shape, m)
			}
		}
	}
	if len(f.tile.shape) == 0 {
		for _, sp := range in {
			for _, m := range sp.comps {
				if !seen[m] && len(f.tile.shape) < shapeBright {
					seen[m] = true
					f.tile.shape = append(f.tile.shape, m)
				}
			}
		}
	}
}

func (f *field) comps(sp *spotPlan, tg, tc, radius float64) []*starModel {
	pl, grid := f.pl, f.grid
	limit := radius * 3600 / f.w.PixelScale()
	type cand struct {
		i int
		d float64
	}
	var cands []cand
	for i, c := range pl {
		d := math.Hypot(c.x-sp.x, c.y-sp.y)
		if d >= compMargin && d <= limit && compOK(c, tg, tc, f.width, f.height) && !crowded(pl, grid, c) {
			cands = append(cands, cand{i, d})
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d < cands[b].d })
	if len(cands) > maxComps {
		cands = cands[:maxComps]
	}
	out := make([]*starModel, 0, len(cands))
	for _, c := range cands {
		out = append(out, f.model(c.i))
	}
	return out
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
func matchStar(pl []placed, grid *discoveries.Grid, sp *spotPlan, scale float64) int {
	star := -1
	best := math.Inf(1)
	for _, i := range grid.Within(sp.x, sp.y, matchWide/scale) {
		d := math.Hypot(pl[i].x-sp.x, pl[i].y-sp.y) * scale
		g := sp.spot.GaiaG
		ok := d <= matchArcsec
		if g != nil {
			ok = math.Abs(pl[i].G-*g) <= matchMag
		}
		if ok && d < best {
			best, star = d, i
		}
	}
	return star
}

// shapeStars are the bright, unsaturated, isolated stars nearest a
// place, to measure each sub's star width and shift there.
func (f *field) shapeStars(x, y float64, n int) []*starModel {
	pl, grid := f.pl, f.grid
	type cand struct {
		i int
		d float64
	}
	var cands []cand
	for i, c := range pl {
		if c.G < shapeBrightest || c.G > shapeFaintest {
			continue
		}
		lonely := true
		for _, j := range grid.Within(c.x, c.y, shapeLonely) {
			o := pl[j]
			if (o.x != c.x || o.y != c.y) && o.G < c.G+shapeNbMag {
				lonely = false
				break
			}
		}
		if lonely {
			cands = append(cands, cand{i, math.Hypot(c.x-x, c.y-y)})
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].d < cands[b].d })
	out := make([]*starModel, 0, min(n, len(cands)))
	for _, c := range cands[:min(n, len(cands))] {
		out = append(out, f.model(c.i))
	}
	return out
}
