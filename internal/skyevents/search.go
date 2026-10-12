// Package skyevents finds sky events: two or more solar-system bodies, or a
// body and deep-sky objects, that fit in one camera frame while the sky is
// dark and they are high enough, over the coming months.
package skyevents

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
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
}

const (
	DefaultSunBelow = -12.0
	DefaultMinAlt   = 15.0
	DefaultStep     = 30 * time.Minute
	// FrameMargin is kept clear on every side of the frame.
	FrameMargin = 0.16
	MergeGap    = 36 * time.Hour
)

// NewCriteria are the defaults for a w by h degree frame.
func NewCriteria(w, h float64) Criteria {
	c := Criteria{SunBelow: DefaultSunBelow, MinAlt: DefaultMinAlt, Step: DefaultStep, StepMin: int(DefaultStep / time.Minute),
		FrameW: w, FrameH: h, UsableW: round(w-2*FrameMargin, 3), UsableH: round(h-2*FrameMargin, 3), MergeGap: MergeGap, FrameEdge: FrameMargin}
	c.Basis = fmt.Sprintf("Every %d minutes while the Sun is below %g° and every member is above %g°; the members fit inside the %.2f° × %.2f° frame less %.2f° on each side (%.2f° × %.2f°) at some rotator angle, and no two are more than %.2f° apart edge to edge. Steps up to %d hours apart join into one event. Positions: Moon and planets from NASA JPL's planetary tables, comets and asteroids from JPL Horizons, deep-sky objects from the stacker catalogue (Messier, and named objects of 20′ or more).",
		c.StepMin, c.SunBelow, c.MinAlt, w, h, FrameMargin, c.UsableW, c.UsableH, c.UsableW, int(MergeGap.Hours()))
	return c
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

type Frame struct {
	CenterRA  float64       `json:"center_ra"`
	CenterDec float64       `json:"center_dec"`
	Rotation  float64       `json:"rotation_deg"`
	Width     float64       `json:"width_deg"`
	Height    float64       `json:"height_deg"`
	Objects   []FrameObject `json:"objects"`
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

// Search finds every event over the grid.
func Search(g Grid, tracks []Track, dsos []DSO, c Criteria) []Event {
	pairs := map[[2]int][]hit{}
	near := make([][]hit, len(tracks))
	for i := range g.Times {
		if !g.Dark[i] {
			continue
		}
		up := []int{}
		for b, t := range tracks {
			if s := t.Samples[i]; s.OK && s.Alt > c.MinAlt {
				up = append(up, b)
			}
		}
		for x, a := range up {
			sa := tracks[a].Samples[i]
			for _, b := range up[x+1:] {
				sb := tracks[b].Samples[i]
				sep := catalog.Separation(sa.RA, sa.Dec, sb.RA, sb.Dec)
				if sep+sa.Diameter/2+sb.Diameter/2 > c.UsableW {
					continue
				}
				if _, ok := fitFrame([]disc{bodyDisc(sa), bodyDisc(sb)}, c.UsableW, c.UsableH); ok {
					pairs[[2]int{a, b}] = append(pairs[[2]int{a, b}], hit{step: i, sep: sep, alt: math.Min(sa.Alt, sb.Alt)})
				}
			}
			if tracks[a].Kind == KindMoon {
				continue
			}
			near[a] = append(near[a], dsoHits(g, i, sa, dsos, c)...)
		}
	}
	var out []Event
	for key, hs := range pairs {
		for _, run := range runs(g, hs, c.MergeGap) {
			out = append(out, bodyBodyEvent(g, tracks, key, run, c))
		}
	}
	for b, hs := range near {
		for _, run := range dsoRuns(g, hs, c.MergeGap) {
			out = append(out, bodyDSOEvent(g, tracks, b, dsos, run, c))
		}
	}
	slices.SortFunc(out, func(a, b Event) int {
		return cmp.Or(a.Best.At.Compare(b.Best.At), cmp.Compare(a.ID, b.ID))
	})
	return out
}

func dsoHits(g Grid, i int, s Sample, dsos []DSO, c Criteria) []hit {
	type cand struct {
		idx int
		sep float64
	}
	var cands []cand
	for k, d := range dsos {
		if math.Abs(d.Dec-s.Dec) > c.UsableW {
			continue
		}
		sep := catalog.Separation(s.RA, s.Dec, d.RA, d.Dec)
		if sep+d.Diameter/2+s.Diameter/2 > c.UsableW {
			continue
		}
		if g.Observers[i].Altitude(d.RA, d.Dec) <= c.MinAlt {
			continue
		}
		cands = append(cands, cand{k, sep})
	}
	slices.SortFunc(cands, func(a, b cand) int { return cmp.Compare(a.sep, b.sep) })
	used := make([]bool, len(cands))
	var out []hit
	for x := range cands {
		if used[x] {
			continue
		}
		group := []disc{bodyDisc(s), dsoDisc(dsos[cands[x].idx])}
		if _, ok := fitFrame(group, c.UsableW, c.UsableH); !ok {
			used[x] = true
			continue
		}
		h := hit{step: i, sep: cands[x].sep, alt: s.Alt, dsos: []int{cands[x].idx}}
		used[x] = true
		for y := x + 1; y < len(cands); y++ {
			if used[y] {
				continue
			}
			try := append(slices.Clone(group), dsoDisc(dsos[cands[y].idx]))
			if _, ok := fitFrame(try, c.UsableW, c.UsableH); ok {
				group = try
				h.dsos = append(h.dsos, cands[y].idx)
				used[y] = true
			}
		}
		out = append(out, h)
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

func (e *Event) finish(g Grid, hs []hit, step time.Duration) {
	first, last := g.Times[hs[0].step], g.Times[hs[len(hs)-1].step]
	e.Start, e.End = first, last.Add(step)
	e.FirstNight, e.LastNight = nightOf(g.Site, first), nightOf(g.Site, last)
	e.Hours = round(float64(len(hs))*step.Hours(), 1)
	byNight := map[string][]hit{}
	var order []string
	for _, h := range hs {
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

func bodyBodyEvent(g Grid, tracks []Track, key [2]int, hs []hit, c Criteria) Event {
	best := hs[0]
	for _, h := range hs {
		if h.sep < best.sep {
			best = h
		}
	}
	idx := orderBodies(tracks, key[:])
	e := Event{Kind: EventBodyBody, MovingCenter: true, bodies: idx}
	names := make([]string, 0, len(idx))
	ds := make([]disc, 0, len(idx))
	objs := make([]FrameObject, 0, len(idx))
	for _, b := range idx {
		s := tracks[b].Samples[best.step]
		e.Members = append(e.Members, bodyMember(tracks[b], s))
		names = append(names, tracks[b].Name)
		ds = append(ds, bodyDisc(s))
		objs = append(objs, FrameObject{Name: tracks[b].Name, Kind: tracks[b].Kind, Diameter: round(s.Diameter, 5)})
	}
	e.Title = names[0] + " with " + strings.Join(names[1:], ", ")
	e.Best = Best{At: g.Times[best.step], Separation: round(best.sep, 3), Altitude: round(best.alt, 1)}
	f, _ := fitFrame(ds, c.UsableW, c.UsableH)
	e.Frame = frameAt(f.center, f.rotation, c, objs, ds)
	e.finish(g, hs, c.Step)
	e.ID = slug(append(names, e.FirstNight)...)
	for i, n := range e.NightWindows {
		var mid []hit
		for _, h := range hs {
			if nightOf(g.Site, g.Times[h.step]) == n.Night {
				mid = append(mid, h)
			}
		}
		step := mid[len(mid)/2].step
		nd := make([]disc, 0, len(idx))
		for _, b := range idx {
			nd = append(nd, bodyDisc(tracks[b].Samples[step]))
		}
		if nf, ok := fitFrame(nd, c.UsableW, c.UsableH); ok {
			e.NightWindows[i].CenterRA, e.NightWindows[i].CenterDec = round(nf.center.RA, 5), round(nf.center.Dec, 5)
		}
	}
	return e
}

func bodyDSOEvent(g Grid, tracks []Track, b int, dsos []DSO, hs []hit, c Criteria) Event {
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
	t := tracks[b]
	s := t.Samples[best.step]
	e := Event{Kind: EventBodyDSO, bodies: []int{b}, dsos: all, Members: []Member{bodyMember(t, s)}}
	names := make([]string, 0, len(all)+1)
	names = append(names, t.Name)
	for _, d := range all {
		e.Members = append(e.Members, dsoMember(dsos[d]))
		names = append(names, dsos[d].Label())
	}
	e.Title = t.Name + " with " + strings.Join(names[1:], ", ")
	e.Best = Best{At: g.Times[best.step], Separation: round(best.sep, 3), Altitude: round(best.alt, 1)}
	ds := make([]disc, 0, len(best.dsos)+1)
	ds = append(ds, bodyDisc(s))
	objs := make([]FrameObject, 0, len(best.dsos)+1)
	objs = append(objs, FrameObject{Name: t.Name, Kind: t.Kind, Diameter: round(s.Diameter, 5)})
	inBest := slices.Clone(best.dsos)
	slices.SortFunc(inBest, func(x, y int) int { return naturalLess(dsos[x].Designation, dsos[y].Designation) })
	e.bestDSOs = inBest
	for _, d := range inBest {
		ds = append(ds, dsoDisc(dsos[d]))
		typ := dsos[d].Type
		objs = append(objs, FrameObject{Name: dsos[d].Label(), Kind: KindDSO, Type: &typ, Diameter: round(dsos[d].Diameter, 4)})
	}
	f, _ := fitFrame(ds, c.UsableW, c.UsableH)
	e.Frame = frameAt(f.center, f.rotation, c, objs, ds)
	e.finish(g, hs, c.Step)
	ids := make([]string, 0, len(all)+2)
	ids = append(ids, t.Name)
	for _, d := range all {
		ids = append(ids, dsos[d].Designation)
	}
	e.ID = slug(append(ids, e.FirstNight)...)
	return e
}
