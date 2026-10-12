// Package skyevents finds sky events: two or more solar-system bodies, or
// bodies and deep-sky objects, that fit in one camera frame or a two-panel
// mosaic while the sky is dark and they are high enough, over the coming
// months.
package skyevents

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const deg = math.Pi / 180

type Kind string

const (
	KindMoon     Kind = "moon"
	KindPlanet   Kind = "planet"
	KindComet    Kind = "comet"
	KindAsteroid Kind = "asteroid"
	KindDSO      Kind = "dso"

	EventBodyDSO  = "body-dso"
	EventBodyBody = "body-body"
	EventMosaic2  = "mosaic-2"

	SourceDE440    = "DE440"
	SourceHorizons = "JPL Horizons"
)

// Criteria are the rules an event meets at each step.
type Criteria struct {
	SunBelow  float64       `json:"sun_below_deg"`
	MinAlt    float64       `json:"min_altitude_deg"`
	Step      time.Duration `json:"-"`
	StepMin   int           `json:"step_minutes"`
	UsableW   float64       `json:"usable_width_deg"`
	UsableH   float64       `json:"usable_height_deg"`
	FrameW    float64       `json:"-"`
	FrameH    float64       `json:"-"`
	MergeGap  time.Duration `json:"-"`
	Basis     string        `json:"basis"`
	FrameEdge float64       `json:"-"`
	Overlap   float64       `json:"mosaic_overlap_pct"`
}

const (
	DefaultSunBelow = -12.0
	DefaultMinAlt   = 15.0
	DefaultStep     = 30 * time.Minute
	// FrameMargin is kept clear on every side of the frame.
	FrameMargin = 0.16
	MergeGap    = 36 * time.Hour
	// MosaicOverlap is the overlap of two-panel event mosaics, in percent,
	// the mosaic planner's default.
	MosaicOverlap  = 15.0
	mosaicIDPrefix = "2-panel"
)

// NewCriteria are the defaults for a w by h degree frame.
func NewCriteria(w, h float64) Criteria {
	c := Criteria{SunBelow: DefaultSunBelow, MinAlt: DefaultMinAlt, Step: DefaultStep, StepMin: int(DefaultStep / time.Minute),
		FrameW: w, FrameH: h, UsableW: round(w-2*FrameMargin, 3), UsableH: round(h-2*FrameMargin, 3), MergeGap: MergeGap, FrameEdge: FrameMargin, Overlap: MosaicOverlap}
	m := c.mosaic()
	c.Basis = fmt.Sprintf("Every %d minutes while the Sun is below %g° and every member is above %g°; the members fit inside the %.2f° × %.2f° frame less %.2f° on each side (%.2f° × %.2f°) at some rotator angle, and no two are more than %.2f° apart edge to edge. When three or more members fit together, that group replaces its smaller groups at the same times. A group that does not fit one frame but fits two frames side by side, overlapping %g%% as the mosaic planner does (%.2f° × %.2f° or %.2f° × %.2f° less the same margin, in any arrangement), is listed as a 2-panel mosaic, unless it fits one frame within %d hours. Steps up to %d hours apart join into one event. Positions: Moon and planets from NASA JPL's planetary tables, comets and asteroids from JPL Horizons, deep-sky objects from the stacker catalogue (Messier, and named objects of 20′ or more).",
		c.StepMin, c.SunBelow, c.MinAlt, w, h, FrameMargin, c.UsableW, c.UsableH, c.UsableW, MosaicOverlap,
		m[0].w+2*FrameMargin, m[0].h+2*FrameMargin, m[1].w+2*FrameMargin, m[1].h+2*FrameMargin, int(MergeGap.Hours()), int(MergeGap.Hours()))
	return c
}

func (c Criteria) frame() (float64, float64) {
	return c.UsableW + 2*FrameMargin, c.UsableH + 2*FrameMargin
}

func (c Criteria) overlap() float64 {
	if c.Overlap > 0 {
		return c.Overlap
	}
	return MosaicOverlap
}

func (c Criteria) single() []shape {
	return []shape{{w: c.UsableW, h: c.UsableH, cols: 1, rows: 1}}
}

// mosaic is two frames side by side, along the long or the short side,
// less the margin around the whole.
func (c Criteria) mosaic() []shape {
	fw, fh := c.frame()
	along := 2 - c.overlap()/100
	return []shape{
		{w: round(along*fw-2*FrameMargin, 3), h: c.UsableH, cols: 2, rows: 1},
		{w: c.UsableW, h: round(along*fh-2*FrameMargin, 3), cols: 1, rows: 2},
	}
}

func (c Criteria) shapes(mosaic bool) []shape {
	if mosaic {
		return c.mosaic()
	}
	return c.single()
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// Sample is one body at one step. Missing samples have OK false.
type Sample struct {
	OK       bool
	RA, Dec  float64
	Alt      float64
	Diameter float64
	Mag      float64
}

// Track is one moving body over the grid.
type Track struct {
	Name        string
	Kind        Kind
	Designation string
	Source      string
	Samples     []Sample
}

// DSO is a catalogue object the bodies may pass.
type DSO struct {
	ID          string
	Designation string
	Name        string
	Type        string
	RA, Dec     float64
	Diameter    float64
	Mag         *float64
}

func (d DSO) Label() string {
	if d.Name == "" || strings.EqualFold(d.Name, d.Designation) {
		return d.Designation
	}
	return d.Designation + " " + d.Name
}

// EventDSOs are the deep-sky objects events use: the Messier list and named
// objects of at least 20 arcmin.
func EventDSOs(objects []catalog.Object) []DSO {
	out := []DSO{}
	for _, o := range objects {
		if !slices.Contains(o.Lists, "messier") && (o.Name == "" || o.MajorArcmin < 20) {
			continue
		}
		out = append(out, DSO{ID: o.ID, Designation: o.Designation, Name: o.Name, Type: o.Type, RA: o.RA, Dec: o.Dec, Diameter: o.MajorArcmin / 60, Mag: o.Magnitude})
	}
	return out
}

// Grid is the search's time steps with an observer for each dark one.
type Grid struct {
	Site      sky.Site
	Times     []time.Time
	Dark      []bool
	Observers []ephem.Observer
}

func NewGrid(e *ephem.Ephemeris, site sky.Site, from, to time.Time, c Criteria) (Grid, error) {
	g := Grid{Site: site}
	for at := from.UTC().Truncate(c.Step); at.Before(to); at = at.Add(c.Step) {
		o := ephem.NewObserver(site, at)
		alt, err := e.SunAltitude(o)
		if err != nil {
			return g, err
		}
		g.Times = append(g.Times, at)
		g.Dark = append(g.Dark, alt < c.SunBelow)
		g.Observers = append(g.Observers, o)
	}
	return g, nil
}

// EphemerisTrack is a body from the embedded ephemeris over the grid.
func EphemerisTrack(e *ephem.Ephemeris, b ephem.Body, g Grid) (Track, error) {
	kind := KindPlanet
	if b == ephem.Moon {
		kind = KindMoon
	}
	t := Track{Name: b.String(), Kind: kind, Source: SourceDE440, Samples: make([]Sample, len(g.Times))}
	for i, o := range g.Observers {
		if !g.Dark[i] {
			continue
		}
		p, err := e.Observe(b, o)
		if err != nil {
			return t, err
		}
		t.Samples[i] = Sample{OK: true, RA: p.RA, Dec: p.Dec, Alt: p.Altitude, Diameter: p.Diameter, Mag: p.Magnitude}
	}
	return t, nil
}

type Member struct {
	Name        string   `json:"name"`
	Kind        Kind     `json:"kind"`
	Designation *string  `json:"designation"`
	Type        *string  `json:"type"`
	Diameter    float64  `json:"diameter_deg"`
	Magnitude   *float64 `json:"magnitude"`
	ID          string   `json:"-"`
}

type Best struct {
	At         time.Time `json:"at"`
	Separation float64   `json:"separation_deg"`
	Altitude   float64   `json:"altitude_deg"`
}

type FrameObject struct {
	Name     string  `json:"name"`
	Kind     Kind    `json:"kind"`
	Type     *string `json:"type"`
	East     float64 `json:"east_deg"`
	North    float64 `json:"north_deg"`
	X        float64 `json:"x_deg"`
	Y        float64 `json:"y_deg"`
	Diameter float64 `json:"diameter_deg"`
}

// Panel is one frame of a mosaic: its centre, its rotation and its offset
// from the mosaic centre, along the frame axes and east and north.
type Panel struct {
	N         int     `json:"n"`
	CenterRA  float64 `json:"center_ra"`
	CenterDec float64 `json:"center_dec"`
	Rotation  float64 `json:"rotation_deg"`
	X         float64 `json:"x_deg"`
	Y         float64 `json:"y_deg"`
	East      float64 `json:"east_deg"`
	North     float64 `json:"north_deg"`
}

// Mosaic is how an event's frames are laid out when one frame is too small.
type Mosaic struct {
	Cols    int     `json:"cols"`
	Rows    int     `json:"rows"`
	Overlap float64 `json:"overlap_pct"`
	Width   float64 `json:"width_deg"`
	Height  float64 `json:"height_deg"`
	Panels  []Panel `json:"panels"`
}

type Frame struct {
	CenterRA  float64       `json:"center_ra"`
	CenterDec float64       `json:"center_dec"`
	Rotation  float64       `json:"rotation_deg"`
	Width     float64       `json:"width_deg"`
	Height    float64       `json:"height_deg"`
	Objects   []FrameObject `json:"objects"`
	Mosaic    *Mosaic       `json:"mosaic,omitempty"`
}

type NightWindow struct {
	Night     string    `json:"night"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Hours     float64   `json:"hours"`
	CenterRA  float64   `json:"center_ra"`
	CenterDec float64   `json:"center_dec"`
}

type Exposure struct {
	ShortSeconds  *float64 `json:"short_seconds"`
	NormalSeconds *float64 `json:"normal_seconds"`
	Basis         string   `json:"basis"`
}

type Event struct {
	ID           string        `json:"id"`
	Kind         string        `json:"kind"`
	Title        string        `json:"title"`
	Members      []Member      `json:"members"`
	FirstNight   string        `json:"first_night"`
	LastNight    string        `json:"last_night"`
	Nights       int           `json:"nights"`
	Start        time.Time     `json:"start"`
	End          time.Time     `json:"end"`
	Best         Best          `json:"best"`
	Hours        float64       `json:"hours_in_frame"`
	Frame        Frame         `json:"frame"`
	MovingCenter bool          `json:"moving_center"`
	NightWindows []NightWindow `json:"night_windows"`
	Exposure     Exposure      `json:"exposure"`
	Priority     string        `json:"priority"`
	Plan         *PlanState    `json:"plan"`
	Rarity       *Rarity       `json:"rarity"`
	AutoPlan     *AutoPlan     `json:"auto_plan"`

	bodies   []int
	dsos     []int
	bestDSOs []int
}

type hit struct {
	step int
	sep  float64
	alt  float64
	dsos []int
}

func nightOf(site sky.Site, t time.Time) string {
	return site.LocalNoon(t).Add(time.Duration(site.Longitude / 15 * float64(time.Hour))).Format(time.DateOnly)
}

func bodyDisc(s Sample) disc { return disc{ra: s.RA, dec: s.Dec, radius: s.Diameter / 2} }

func dsoDisc(d DSO) disc { return disc{ra: d.RA, dec: d.Dec, radius: d.Diameter / 2} }

// group is a set of members at one step: bodies first, then deep-sky
// objects, with their discs in the same order.
type group struct {
	bodies []int
	dsos   []int
	ds     []disc
}

func bodiesKey(bodies []int) string {
	parts := make([]string, len(bodies))
	for i, b := range bodies {
		parts[i] = strconv.Itoa(b)
	}
	return strings.Join(parts, ",")
}

func (gr group) within(o group) bool {
	for _, b := range gr.bodies {
		if !slices.Contains(o.bodies, b) {
			return false
		}
	}
	for _, d := range gr.dsos {
		if !slices.Contains(o.dsos, d) {
			return false
		}
	}
	return true
}

// spread is the figure an event reports: the widest separation of its
// bodies, or for one body the separation from it to the nearest deep-sky
// object.
func spread(ds []disc, bodies int) float64 {
	if bodies >= 2 {
		widest := 0.0
		for i := range bodies {
			for j := i + 1; j < bodies; j++ {
				widest = math.Max(widest, catalog.Separation(ds[i].ra, ds[i].dec, ds[j].ra, ds[j].dec))
			}
		}
		return widest
	}
	nearest := math.Inf(1)
	for _, d := range ds[1:] {
		nearest = math.Min(nearest, catalog.Separation(ds[0].ra, ds[0].dec, d.ra, d.dec))
	}
	return nearest
}

// step is one dark time step of the search.
type step struct {
	g      Grid
	i      int
	tracks []Track
	up     []int
	dsos   []DSO
	c      Criteria
	alt    map[int]bool
}

func (s *step) dsoUp(k int) bool {
	if v, ok := s.alt[k]; ok {
		return v
	}
	d := s.dsos[k]
	v := s.g.Observers[s.i].Altitude(d.RA, d.Dec) > s.c.MinAlt
	s.alt[k] = v
	return v
}

// bodySets is every set of bodies up at the step that fits the shapes.
func (s *step) bodySets(shapes []shape) []group {
	long := longest(shapes)
	var out []group
	var grow func(cur group, from int)
	grow = func(cur group, from int) {
		for x := from; x < len(s.up); x++ {
			b := s.up[x]
			d := bodyDisc(s.tracks[b].Samples[s.i])
			if slices.ContainsFunc(cur.ds, func(o disc) bool {
				return catalog.Separation(o.ra, o.dec, d.ra, d.dec)+o.radius+d.radius > long
			}) {
				continue
			}
			next := group{bodies: append(slices.Clone(cur.bodies), b), ds: append(slices.Clone(cur.ds), d)}
			if len(next.ds) > 1 {
				if _, _, ok := fitShapes(next.ds, shapes); !ok {
					continue
				}
			}
			out = append(out, next)
			grow(next, x+1)
		}
	}
	grow(group{}, 0)
	return out
}

// dsoGroups adds deep-sky objects to a set of bodies: nearest first, each
// object starts a group and takes in every later one that still fits.
func (s *step) dsoGroups(set group, shapes []shape) []group {
	type cand struct {
		idx int
		sep float64
	}
	long := longest(shapes)
	mid := meanPoint(set.ds)
	var cands []cand
	for k, d := range s.dsos {
		if math.Abs(d.Dec-mid.Dec) > long {
			continue
		}
		dd := dsoDisc(d)
		if slices.ContainsFunc(set.ds, func(o disc) bool {
			return catalog.Separation(o.ra, o.dec, dd.ra, dd.dec)+o.radius+dd.radius > long
		}) {
			continue
		}
		if !s.dsoUp(k) {
			continue
		}
		cands = append(cands, cand{k, catalog.Separation(mid.RA, mid.Dec, d.RA, d.Dec)})
	}
	slices.SortStableFunc(cands, func(a, b cand) int { return cmp.Compare(a.sep, b.sep) })
	used := make([]bool, len(cands))
	var out []group
	for x := range cands {
		if used[x] {
			continue
		}
		used[x] = true
		gr := group{bodies: set.bodies, dsos: []int{cands[x].idx}, ds: append(slices.Clone(set.ds), dsoDisc(s.dsos[cands[x].idx]))}
		if _, _, ok := fitShapes(gr.ds, shapes); !ok {
			continue
		}
		for y := x + 1; y < len(cands); y++ {
			if used[y] {
				continue
			}
			try := append(slices.Clone(gr.ds), dsoDisc(s.dsos[cands[y].idx]))
			if _, _, ok := fitShapes(try, shapes); ok {
				gr.ds = try
				gr.dsos = append(gr.dsos, cands[y].idx)
				used[y] = true
			}
		}
		out = append(out, gr)
	}
	return out
}

func (s *step) hasMoon(bodies []int) bool {
	return slices.ContainsFunc(bodies, func(b int) bool { return s.tracks[b].Kind == KindMoon })
}

// groups is every group at the step that fits the shapes, and the ones no
// larger group holds. The Moon is never grouped with deep-sky objects.
func (s *step) groups(shapes []shape) (sets, all, top []group) {
	sets = s.bodySets(shapes)
	for _, set := range sets {
		if len(set.bodies) > 1 {
			all = append(all, set)
		}
		if !s.hasMoon(set.bodies) {
			all = append(all, s.dsoGroups(set, shapes)...)
		}
	}
	for i, a := range all {
		held := false
		for j, b := range all {
			if i != j && a.within(b) && (!b.within(a) || j < i) {
				held = true
				break
			}
		}
		if !held {
			top = append(top, a)
		}
	}
	return sets, all, top
}

func (s *step) hit(gr group) hit {
	alt := math.Inf(1)
	for _, b := range gr.bodies {
		alt = math.Min(alt, s.tracks[b].Samples[s.i].Alt)
	}
	return hit{step: s.i, sep: spread(gr.ds, len(gr.bodies)), alt: alt, dsos: slices.Clone(gr.dsos)}
}

// series is the hits of one set of bodies, alone or with deep-sky objects,
// in one frame or a mosaic.
type series struct {
	bodies []int
	dso    bool
	mosaic bool
	hits   []hit
}

func seriesKey(mosaic, dso bool, bodies []int) string {
	return fmt.Sprintf("%t|%t|%s", mosaic, dso, bodiesKey(bodies))
}

// collector gathers the series, and for each set of bodies every step it
// fits one frame, with the deep-sky objects it takes there, whether or not
// a larger group holds it.
type collector struct {
	series map[string]*series
	narrow map[string][]hit
}

func fitKey(dso bool, bodies []int) string {
	return fmt.Sprintf("%t|%s", dso, bodiesKey(bodies))
}

func (a *collector) note(sets, all []group, s *step) {
	to := a.narrow
	for _, set := range sets {
		if len(set.bodies) > 1 {
			k := fitKey(false, set.bodies)
			to[k] = append(to[k], hit{step: s.i})
		}
	}
	for _, gr := range all {
		if len(gr.dsos) > 0 {
			k := fitKey(true, gr.bodies)
			to[k] = append(to[k], hit{step: s.i, dsos: gr.dsos})
		}
	}
}

func (a *collector) add(mosaic bool, gr group, h hit) {
	k := seriesKey(mosaic, len(gr.dsos) > 0, gr.bodies)
	sr, ok := a.series[k]
	if !ok {
		sr = &series{bodies: gr.bodies, dso: len(gr.dsos) > 0, mosaic: mosaic}
		a.series[k] = sr
	}
	sr.hits = append(sr.hits, h)
}

// Search finds every event over the grid.
func Search(g Grid, tracks []Track, dsos []DSO, c Criteria) []Event {
	single, mosaic := c.single(), c.mosaic()
	acc := &collector{series: map[string]*series{}, narrow: map[string][]hit{}}
	for i := range g.Times {
		if !g.Dark[i] {
			continue
		}
		s := &step{g: g, i: i, tracks: tracks, dsos: dsos, c: c, alt: map[int]bool{}}
		for b, t := range tracks {
			if smp := t.Samples[i]; smp.OK && smp.Alt > c.MinAlt {
				s.up = append(s.up, b)
			}
		}
		sets, all, top := s.groups(single)
		acc.note(sets, all, s)
		fitsHere := map[string]bool{}
		for _, set := range sets {
			fitsHere[bodiesKey(set.bodies)] = true
		}
		for _, gr := range top {
			acc.add(false, gr, s.hit(gr))
		}
		_, _, wide := s.groups(mosaic)
		for _, gr := range wide {
			if len(gr.dsos) == 0 && fitsHere[bodiesKey(gr.bodies)] {
				continue
			}
			if len(gr.dsos) > 0 {
				if _, _, ok := fitShapes(gr.ds, single); ok {
					continue
				}
			}
			acc.add(true, gr, s.hit(gr))
		}
	}
	keys := make([]string, 0, len(acc.series))
	for k := range acc.series {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []Event
	for _, k := range keys {
		sr := acc.series[k]
		if sr.mosaic {
			if sr.hits = acc.clearOfOneFrame(g, sr, out, c.MergeGap+c.Step); len(sr.hits) == 0 {
				continue
			}
		}
		var rs [][]hit
		if sr.dso {
			rs = dsoRuns(g, sr.hits, c.MergeGap)
		} else {
			rs = runs(g, sr.hits, c.MergeGap)
		}
		for _, run := range rs {
			out = append(out, groupEvent(g, tracks, dsos, sr, run, c))
		}
	}
	slices.SortFunc(out, func(a, b Event) int {
		return cmp.Or(a.Best.At.Compare(b.Best.At), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// clearOfOneFrame keeps the mosaic hits of a series at steps where its
// members fit no single frame within the gap, and that no one-frame event
// of the same bodies holding the same deep-sky objects spans within the
// gap, so a mosaic event never repeats a one-frame event of its members
// and does not depend on how far the search looks. The one-frame events
// are built first.
func (a *collector) clearOfOneFrame(g Grid, sr *series, single []Event, gap time.Duration) []hit {
	key := bodiesKey(sr.bodies)
	var spans []Event
	for _, e := range single {
		if e.Kind != EventMosaic2 && (e.Kind == EventBodyDSO) == sr.dso && bodiesKey(slices.Sorted(slices.Values(e.bodies))) == key {
			spans = append(spans, e)
		}
	}
	holds := func(have, want []int) bool {
		return !slices.ContainsFunc(want, func(d int) bool { return !slices.Contains(have, d) })
	}
	near := a.narrow[fitKey(sr.dso, sr.bodies)]
	out := make([]hit, 0, len(sr.hits))
	for _, h := range sr.hits {
		at := g.Times[h.step]
		from, to := at.Add(-gap), at.Add(gap)
		covered := slices.ContainsFunc(spans, func(e Event) bool {
			return !e.Start.After(to) && !e.End.Before(from) && holds(e.dsos, h.dsos)
		})
		lo := sort.Search(len(near), func(i int) bool { return !g.Times[near[i].step].Before(from) })
		for _, n := range near[lo:] {
			if covered || g.Times[n.step].After(to) {
				break
			}
			covered = holds(n.dsos, h.dsos)
		}
		if !covered {
			out = append(out, h)
		}
	}
	return out
}

func runs(g Grid, hs []hit, gap time.Duration) [][]hit {
	var out [][]hit
	cur := make([]hit, 0, len(hs))
	for _, h := range hs {
		if len(cur) > 0 && g.Times[h.step].Sub(g.Times[cur[len(cur)-1].step]) > gap {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, h)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func dsoRuns(g Grid, hs []hit, gap time.Duration) [][]hit {
	type open struct {
		hits []hit
		set  map[int]bool
		last time.Time
		done bool
	}
	var events []*open
	for _, h := range hs {
		at := g.Times[h.step]
		var into *open
		for _, e := range events {
			if e.done {
				continue
			}
			if at.Sub(e.last) > gap {
				e.done = true
				continue
			}
			if !slices.ContainsFunc(h.dsos, func(d int) bool { return e.set[d] }) {
				continue
			}
			if into == nil {
				into = e
				continue
			}
			into.hits = append(into.hits, e.hits...)
			for d := range e.set {
				into.set[d] = true
			}
			into.last = maxTime(into.last, e.last)
			e.done, e.hits = true, nil
		}
		if into == nil {
			into = &open{set: map[int]bool{}}
			events = append(events, into)
		}
		into.hits = append(into.hits, h)
		into.last = at
		for _, d := range h.dsos {
			into.set[d] = true
		}
	}
	var out [][]hit
	for _, e := range events {
		if len(e.hits) == 0 {
			continue
		}
		slices.SortStableFunc(e.hits, func(a, b hit) int { return cmp.Compare(a.step, b.step) })
		out = append(out, e.hits)
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func bodyOrder(t Track) int {
	switch t.Kind {
	case KindMoon:
		return 0
	case KindPlanet:
		return 1
	case KindComet, KindAsteroid, KindDSO:
		return 2
	}
	return 3
}

func planetIndex(name string) int {
	for i, b := range ephem.Planets() {
		if b.String() == name {
			return i
		}
	}
	return len(ephem.Planets())
}

func orderBodies(tracks []Track, idx []int) []int {
	out := slices.Clone(idx)
	slices.SortFunc(out, func(a, b int) int {
		return cmp.Or(cmp.Compare(bodyOrder(tracks[a]), bodyOrder(tracks[b])), cmp.Compare(planetIndex(tracks[a].Name), planetIndex(tracks[b].Name)),
			cmp.Compare(tracks[a].Name, tracks[b].Name))
	})
	return out
}

var designationNumber = regexp.MustCompile(`^(\D*?)\s*(\d+)(.*)$`)

func naturalLess(a, b string) int {
	ma, mb := designationNumber.FindStringSubmatch(a), designationNumber.FindStringSubmatch(b)
	if ma != nil && mb != nil && ma[1] == mb[1] {
		na, _ := strconv.Atoi(ma[2])
		nb, _ := strconv.Atoi(mb[2])
		if na != nb {
			return cmp.Compare(na, nb)
		}
	}
	return cmp.Compare(a, b)
}

func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	r := round(v, 2)
	return &r
}

func bodyMember(t Track, s Sample) Member {
	m := Member{Name: t.Name, Kind: t.Kind, Diameter: round(s.Diameter, 5), Magnitude: finite(s.Mag), ID: "body:" + strings.ToLower(t.Name)}
	if t.Designation != "" {
		d := t.Designation
		m.Designation = &d
		m.ID = "body:" + strings.ToLower(d)
	}
	return m
}

func dsoMember(d DSO) Member {
	des, typ := d.Designation, d.Type
	m := Member{Name: d.Label(), Kind: KindDSO, Designation: &des, Diameter: round(d.Diameter, 4), ID: "dso:" + d.ID}
	if typ != "" {
		m.Type = &typ
	}
	if d.Mag != nil {
		m.Magnitude = finite(*d.Mag)
	}
	return m
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

func slug(parts ...string) string {
	return strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(strings.Join(parts, " ")), "-"), "-")
}

func frameAt(center mosaics.Point, rotation float64, c Criteria, objs []FrameObject, ds []disc) Frame {
	f := Frame{CenterRA: round(center.RA, 5), CenterDec: round(center.Dec, 5), Rotation: round(rotation, 1), Width: c.FrameW, Height: c.FrameH, Objects: objs}
	for i := range f.Objects {
		e, n := mosaics.Project(center, mosaics.Point{RA: ds[i].ra, Dec: ds[i].dec})
		x, y := frameXY(e, n, rotation)
		f.Objects[i].East, f.Objects[i].North, f.Objects[i].X, f.Objects[i].Y = round(e, 4), round(n, 4), round(x, 4), round(y, 4)
	}
	return f
}

// finish fills the window, the hours and the nights from the run's steps,
// counting a step once however many groups of the run it holds.
func (e *Event) finish(g Grid, hs []hit, step time.Duration) {
	steps := make([]hit, 0, len(hs))
	for _, h := range hs {
		if len(steps) == 0 || steps[len(steps)-1].step != h.step {
			steps = append(steps, h)
		}
	}
	first, last := g.Times[steps[0].step], g.Times[steps[len(steps)-1].step]
	e.Start, e.End = first, last.Add(step)
	e.FirstNight, e.LastNight = nightOf(g.Site, first), nightOf(g.Site, last)
	e.Hours = round(float64(len(steps))*step.Hours(), 1)
	byNight := map[string][]hit{}
	var order []string
	for _, h := range steps {
		n := nightOf(g.Site, g.Times[h.step])
		if _, ok := byNight[n]; !ok {
			order = append(order, n)
		}
		byNight[n] = append(byNight[n], h)
	}
	e.Nights = len(order)
	for _, n := range order {
		nh := byNight[n]
		e.NightWindows = append(e.NightWindows, NightWindow{Night: n, Start: g.Times[nh[0].step], End: g.Times[nh[len(nh)-1].step].Add(step),
			Hours: round(float64(len(nh))*step.Hours(), 1), CenterRA: e.Frame.CenterRA, CenterDec: e.Frame.CenterDec})
	}
	e.Priority = PriorityNormal
}

func listNames(names []string) string {
	return names[0] + " with " + strings.Join(names[1:], ", ")
}

// groupEvent builds the event of one run of a series. A series without
// deep-sky objects follows the bodies with a moving frame centre.
func groupEvent(g Grid, tracks []Track, dsos []DSO, sr *series, hs []hit, c Criteria) Event {
	best := hs[0]
	for _, h := range hs {
		if len(h.dsos) > len(best.dsos) || (len(h.dsos) == len(best.dsos) && h.sep < best.sep) {
			best = h
		}
	}
	set := map[int]bool{}
	for _, h := range hs {
		for _, d := range h.dsos {
			set[d] = true
		}
	}
	all := make([]int, 0, len(set))
	for d := range set {
		all = append(all, d)
	}
	slices.SortFunc(all, func(x, y int) int { return naturalLess(dsos[x].Designation, dsos[y].Designation) })
	idx := orderBodies(tracks, sr.bodies)
	e := Event{Kind: EventBodyBody, bodies: idx, dsos: all}
	switch {
	case sr.mosaic:
		e.Kind = EventMosaic2
	case sr.dso:
		e.Kind = EventBodyDSO
	}
	e.MovingCenter = !sr.dso
	names := make([]string, 0, len(idx)+len(all))
	ids := make([]string, 0, len(idx)+len(all)+2)
	if sr.mosaic {
		ids = append(ids, mosaicIDPrefix)
	}
	ds := make([]disc, 0, len(idx)+len(best.dsos))
	objs := make([]FrameObject, 0, len(idx)+len(best.dsos))
	for _, b := range idx {
		smp := tracks[b].Samples[best.step]
		e.Members = append(e.Members, bodyMember(tracks[b], smp))
		names = append(names, tracks[b].Name)
		ids = append(ids, tracks[b].Name)
		ds = append(ds, bodyDisc(smp))
		objs = append(objs, FrameObject{Name: tracks[b].Name, Kind: tracks[b].Kind, Diameter: round(smp.Diameter, 5)})
	}
	for _, d := range all {
		e.Members = append(e.Members, dsoMember(dsos[d]))
		names = append(names, dsos[d].Label())
		ids = append(ids, dsos[d].Designation)
	}
	inBest := slices.Clone(best.dsos)
	slices.SortFunc(inBest, func(x, y int) int { return naturalLess(dsos[x].Designation, dsos[y].Designation) })
	e.bestDSOs = inBest
	for _, d := range inBest {
		ds = append(ds, dsoDisc(dsos[d]))
		typ := dsos[d].Type
		objs = append(objs, FrameObject{Name: dsos[d].Label(), Kind: KindDSO, Type: &typ, Diameter: round(dsos[d].Diameter, 4)})
	}
	e.Title = listNames(names)
	e.Best = Best{At: g.Times[best.step], Separation: round(best.sep, 3), Altitude: round(best.alt, 1)}
	shapes := c.shapes(sr.mosaic)
	f, at, ok := fitShapes(ds, shapes)
	if !ok {
		f, at = fit{center: meanPoint(ds)}, 0
	}
	if e.MovingCenter && ok {
		f.rotation, hs = steadyRotation(tracks, idx, hs, shapes[at], f.rotation)
		f.center = centreAt(ds, f.rotation)
	}
	e.Frame = frameAt(f.center, f.rotation, c, objs, ds)
	if sr.mosaic {
		e.Frame.Mosaic = mosaicFrame(f.center, e.Frame.Rotation, shapes[at], c)
	}
	e.finish(g, hs, c.Step)
	e.ID = slug(append(ids, e.FirstNight)...)
	if !e.MovingCenter {
		return e
	}
	for i, n := range e.NightWindows {
		var mid []hit
		for _, x := range hs {
			if nightOf(g.Site, g.Times[x.step]) == n.Night {
				mid = append(mid, x)
			}
		}
		st := mid[len(mid)/2].step
		nd := make([]disc, 0, len(idx))
		for _, b := range idx {
			nd = append(nd, bodyDisc(tracks[b].Samples[st]))
		}
		if _, _, ok := fitShapes(nd, shapes); ok {
			ctr := centreAt(nd, e.Frame.Rotation)
			e.NightWindows[i].CenterRA, e.NightWindows[i].CenterDec = round(ctr.RA, 5), round(ctr.Dec, 5)
		}
	}
	return e
}

// mosaicFrame lays out the panels of a mosaic around its centre the way the
// mosaic planner does.
func mosaicFrame(center mosaics.Point, rotation float64, sh shape, c Criteria) *Mosaic {
	fw, fh := c.frame()
	o := c.overlap()
	l := mosaics.Grid(mosaics.Outline{Centre: center}, sh.rows, sh.cols, rotation, o, mosaics.Rig{WidthDeg: fw, HeightDeg: fh})
	m := &Mosaic{Cols: sh.cols, Rows: sh.rows, Overlap: o, Width: round(sh.w+2*FrameMargin, 3), Height: round(sh.h+2*FrameMargin, 3)}
	for _, p := range l.Panels {
		e, n := mosaics.Project(center, p.Centre)
		x, y := frameXY(e, n, rotation)
		m.Panels = append(m.Panels, Panel{N: p.N, CenterRA: round(p.Centre.RA, 5), CenterDec: round(p.Centre.Dec, 5), Rotation: round(p.RotationDeg, 2),
			X: round(x, 4), Y: round(y, 4), East: round(e, 4), North: round(n, 4)})
	}
	return m
}

// steadyRotation picks the rotator angle, to 0.1°, that holds the bodies in
// the shape at the most steps of the run, keeping the best moment's own
// angle on a tie, and returns the run's steps that fit at it. The
// scheduler turns the camera once for a target, so the event's hours and
// nights count only the steps the frame holds the bodies at that angle.
func steadyRotation(tracks []Track, bodies []int, hs []hit, sh shape, preferred float64) (float64, []hit) {
	type at struct {
		step int
		p    projected
	}
	var steps []at
	for _, h := range hs {
		if len(steps) > 0 && steps[len(steps)-1].step == h.step {
			continue
		}
		ds := make([]disc, 0, len(bodies))
		for _, b := range bodies {
			ds = append(ds, bodyDisc(tracks[b].Samples[h.step]))
		}
		steps = append(steps, at{h.step, project(ds)})
	}
	holds := func(x at, r float64) bool {
		s, _, _ := x.p.box(r, sh.w, sh.h)
		return s >= 0 && x.p.pairwise <= sh.reach()
	}
	best, most := round(preferred, 1), -1
	for k := -1; k < 180; k++ {
		r := best
		if k >= 0 {
			r = float64(k)
		}
		n := 0
		for _, x := range steps {
			if holds(x, r) {
				n++
			}
		}
		if n > most {
			best, most = r, n
		}
	}
	keep := map[int]bool{}
	for _, x := range steps {
		if holds(x, best) {
			keep[x.step] = true
		}
	}
	out := make([]hit, 0, len(hs))
	for _, h := range hs {
		if keep[h.step] {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return round(preferred, 1), hs
	}
	return best, out
}
