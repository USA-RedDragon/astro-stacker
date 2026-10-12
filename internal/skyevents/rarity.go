package skyevents

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

const (
	// RecurrenceYears is how far before and after an event the recurrence
	// search looks.
	RecurrenceYears = 50
	// RareYears is the gap both ways that makes an event rare.
	RareYears = 10
	// CoarseStep is the step of the first pass over the decades.
	CoarseStep    = 24 * time.Hour
	rarityVersion = 1
	yearDays      = 365.25
	moonParallax  = 2.0
)

// maxSpeed is an upper bound on a body's daily motion across the sky, in
// degrees, as seen from Earth.
func maxSpeed(b ephem.Body) float64 {
	switch b {
	case ephem.Moon:
		return 16.5
	case ephem.Mercury:
		return 2.4
	case ephem.Venus:
		return 1.4
	case ephem.Mars:
		return 0.9
	case ephem.Jupiter:
		return 0.3
	case ephem.Saturn:
		return 0.2
	case ephem.Sun:
		return 1.1
	case ephem.EMB, ephem.Earth, ephem.Uranus, ephem.Neptune:
		return 0.1
	}
	return 20
}

// Occurrence is one other time the same members share the frame.
type Occurrence struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	FirstNight string    `json:"first_night"`
	LastNight  string    `json:"last_night"`
	Best       time.Time `json:"best"`
	Separation float64   `json:"separation_deg"`
}

// Rarity is how often an event's members share the frame under the same
// rules.
type Rarity struct {
	Status        string      `json:"status"`
	Reason        *string     `json:"reason"`
	Key           string      `json:"key"`
	SearchedFrom  *time.Time  `json:"searched_from"`
	SearchedTo    *time.Time  `json:"searched_to"`
	Previous      *Occurrence `json:"previous"`
	Next          *Occurrence `json:"next"`
	PreviousYears *float64    `json:"previous_years"`
	NextYears     *float64    `json:"next_years"`
	IntervalYears *float64    `json:"interval_years"`
	Rare          bool        `json:"rare"`
	Method        string      `json:"method"`
	Basis         string      `json:"basis"`
}

const (
	MethodSearch = "search"
	MethodOrbit  = "orbit"
	// LifetimeYears is the orbital period beyond which a comet counts as
	// coming back once in a lifetime.
	LifetimeYears = 50
)

// IsRare reports whether the event's rarity was searched and found rare.
func (e Event) IsRare() bool {
	return e.Rarity != nil && e.Rarity.Status == StatusReady && e.Rarity.Rare
}

func rarityUnavailable(reason string) *Rarity {
	return &Rarity{Status: StatusUnavailable, Reason: &reason}
}

// pairing is the fixed set of members whose recurrence is searched: the
// bodies and the deep-sky objects of the event's best frame.
type pairing struct {
	key    string
	bodies []ephem.Body
	dsos   []DSO
}

func ephemerisBody(name string) (ephem.Body, bool) {
	for _, b := range append([]ephem.Body{ephem.Moon}, ephem.Planets()...) {
		if b.String() == name {
			return b, true
		}
	}
	return 0, false
}

func pairingOf(e Event, dsos []DSO) (pairing, string) {
	var p pairing
	var ids []string
	for _, m := range e.Members {
		switch m.Kind {
		case KindMoon, KindPlanet:
			b, ok := ephemerisBody(m.Name)
			if !ok {
				return p, "No ephemeris for " + m.Name + "."
			}
			p.bodies = append(p.bodies, b)
			ids = append(ids, m.ID)
		case KindComet, KindAsteroid:
			return p, "Recurrence of a comet or asteroid pairing comes from its orbit."
		case KindDSO:
		}
	}
	for _, i := range e.bestDSOs {
		if i < 0 || i >= len(dsos) {
			return p, "The event's deep-sky objects are not in the catalogue."
		}
		p.dsos = append(p.dsos, dsos[i])
		ids = append(ids, "dso:"+dsos[i].ID)
	}
	if len(p.bodies) == 0 || len(p.bodies)+len(p.dsos) < 2 {
		return p, "The event has no fixed pairing to search for."
	}
	sort.Strings(ids)
	p.key = strings.Join(ids, "|")
	return p, ""
}

// rarityKey names everything a rarity result depends on, so a later search
// reuses it while nothing changed.
func rarityKey(p pairing, e Event, site sky.Site, c Criteria, from, to time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "v%d|%s|%s|%s|%s|%.4f,%.4f", rarityVersion, p.key, e.Best.At.UTC().Format(time.RFC3339), e.Start.UTC().Format(time.RFC3339),
		e.End.UTC().Format(time.RFC3339), site.Latitude, site.Longitude)
	if site.Elevation != nil {
		fmt.Fprintf(&b, ",%.0f", *site.Elevation)
	}
	fmt.Fprintf(&b, "|%.3f,%.3f,%g,%g,%d,%s|%s,%s", c.UsableW, c.UsableH, c.MinAlt, c.SunBelow, c.StepMin, c.MergeGap, from.Format(time.DateOnly), to.Format(time.DateOnly))
	for _, d := range p.dsos {
		fmt.Fprintf(&b, "|%s:%.5f,%.5f,%.4f", d.ID, d.RA, d.Dec, d.Diameter)
	}
	return b.String()
}

func years(d time.Duration) float64 { return d.Hours() / 24 / yearDays }

// recurrenceSpan is the part of ±RecurrenceYears around t the ephemeris
// covers, kept two days clear of its ends.
func recurrenceSpan(e *ephem.Ephemeris, t time.Time) (time.Time, time.Time) {
	lo, hi := e.Range()
	span := time.Duration(RecurrenceYears * yearDays * 24 * float64(time.Hour))
	from, to := t.Add(-span), t.Add(span)
	if first := lo.Add(48 * time.Hour); from.Before(first) {
		from = first
	}
	if last := hi.Add(-48 * time.Hour); to.After(last) {
		to = last
	}
	return from.UTC().Truncate(24 * time.Hour), to.UTC().Truncate(24 * time.Hour)
}

type coarseSample struct{ ra, dec float64 }

// scanner finds every time a pairing shares the frame over decades: a daily
// pass rules out the days its members are too far apart for any moment of
// the day to fit, then the event grid's steps are checked on the rest.
type scanner struct {
	eph  *ephem.Ephemeris
	site sky.Site
	c    Criteria
	base time.Time
	days int

	mu     sync.Mutex
	coarse map[ephem.Body][]coarseSample
	dark   map[int64]bool
}

func newScanner(e *ephem.Ephemeris, site sky.Site, c Criteria, from, to time.Time) *scanner {
	midnight := time.Duration((12 - site.Longitude/15) * float64(time.Hour))
	base := from.UTC().Truncate(24 * time.Hour).Add(midnight)
	if base.After(from) {
		base = base.Add(-CoarseStep)
	}
	days := int((to.Sub(base)+CoarseStep/2)/CoarseStep) + 1
	return &scanner{eph: e, site: site, c: c, base: base, days: days, coarse: map[ephem.Body][]coarseSample{}, dark: map[int64]bool{}}
}

func (s *scanner) day(i int) time.Time { return s.base.Add(time.Duration(i) * CoarseStep) }

func (s *scanner) track(b ephem.Body) ([]coarseSample, error) {
	s.mu.Lock()
	if t, ok := s.coarse[b]; ok {
		s.mu.Unlock()
		return t, nil
	}
	s.mu.Unlock()
	out := make([]coarseSample, s.days)
	for i := range out {
		p, err := s.eph.Observe(b, ephem.NewObserver(s.site, s.day(i)))
		if err != nil {
			return nil, err
		}
		out[i] = coarseSample{p.RA, p.Dec}
	}
	s.mu.Lock()
	s.coarse[b] = out
	s.mu.Unlock()
	return out, nil
}

func (s *scanner) isDark(o ephem.Observer) (bool, error) {
	k := o.At.Unix()
	s.mu.Lock()
	d, ok := s.dark[k]
	s.mu.Unlock()
	if ok {
		return d, nil
	}
	alt, err := s.eph.SunAltitude(o)
	if err != nil {
		return false, err
	}
	d = alt < s.c.SunBelow
	s.mu.Lock()
	s.dark[k] = d
	s.mu.Unlock()
	return d, nil
}

// margin is how much closer the members can get during the day around a
// daily sample than at it.
func margin(bodies []ephem.Body) float64 {
	m := 0.05
	for _, b := range bodies {
		m += maxSpeed(b) * CoarseStep.Hours() / 24 / 2
		if b == ephem.Moon {
			m += moonParallax
		}
	}
	return m
}

type rhit struct {
	at  time.Time
	sep float64
}

// scan returns every step from..to at which the pairing fits.
func (s *scanner) scan(p pairing, from, to time.Time) ([]rhit, error) {
	tracks := make([][]coarseSample, len(p.bodies))
	for i, b := range p.bodies {
		t, err := s.track(b)
		if err != nil {
			return nil, err
		}
		tracks[i] = t
	}
	limit := s.c.UsableW + margin(p.bodies)
	var out []rhit
	half := CoarseStep / 2
	for i := range s.days {
		mid := s.day(i)
		if mid.Add(half).Before(from) || !mid.Add(-half).Before(to) {
			continue
		}
		if !s.candidate(p, tracks, i, limit) {
			continue
		}
		start := mid.Add(-half).Truncate(s.c.Step)
		if start.Before(mid.Add(-half)) {
			start = start.Add(s.c.Step)
		}
		for at := start; at.Before(mid.Add(half)); at = at.Add(s.c.Step) {
			if at.Before(from) || !at.Before(to) {
				continue
			}
			h, ok, err := s.fits(p, at)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, h)
			}
		}
	}
	return out, nil
}

func (s *scanner) candidate(p pairing, tracks [][]coarseSample, i int, limit float64) bool {
	for a := range tracks {
		for b := a + 1; b < len(tracks); b++ {
			if catalog.Separation(tracks[a][i].ra, tracks[a][i].dec, tracks[b][i].ra, tracks[b][i].dec) > limit {
				return false
			}
		}
		for _, d := range p.dsos {
			if catalog.Separation(tracks[a][i].ra, tracks[a][i].dec, d.RA, d.Dec) > limit {
				return false
			}
		}
	}
	return true
}

func (s *scanner) fits(p pairing, at time.Time) (rhit, bool, error) {
	o := ephem.NewObserver(s.site, at)
	dark, err := s.isDark(o)
	if err != nil || !dark {
		return rhit{}, false, err
	}
	ds := make([]disc, 0, len(p.bodies)+len(p.dsos))
	for _, b := range p.bodies {
		pos, err := s.eph.Observe(b, o)
		if err != nil {
			return rhit{}, false, err
		}
		if pos.Altitude <= s.c.MinAlt {
			return rhit{}, false, nil
		}
		ds = append(ds, disc{ra: pos.RA, dec: pos.Dec, radius: pos.Diameter / 2})
	}
	for _, d := range p.dsos {
		if o.Altitude(d.RA, d.Dec) <= s.c.MinAlt {
			return rhit{}, false, nil
		}
		ds = append(ds, dsoDisc(d))
	}
	if _, ok := fitFrame(ds, s.c.UsableW, s.c.UsableH); !ok {
		return rhit{}, false, nil
	}
	sep := math.Inf(1)
	if len(p.dsos) == 0 {
		sep = catalog.Separation(ds[0].ra, ds[0].dec, ds[1].ra, ds[1].dec)
	} else {
		for _, d := range ds[len(p.bodies):] {
			sep = math.Min(sep, catalog.Separation(ds[0].ra, ds[0].dec, d.ra, d.dec))
		}
	}
	return rhit{at: at, sep: sep}, true, nil
}

func (s *scanner) occurrences(hs []rhit) []Occurrence {
	var out []Occurrence
	cur := make([]rhit, 0, len(hs))
	flush := func() {
		if len(cur) == 0 {
			return
		}
		best := cur[0]
		for _, h := range cur {
			if h.sep < best.sep {
				best = h
			}
		}
		out = append(out, Occurrence{Start: cur[0].at, End: cur[len(cur)-1].at.Add(s.c.Step), FirstNight: nightOf(s.site, cur[0].at), LastNight: nightOf(s.site, cur[len(cur)-1].at), Best: best.at, Separation: round(best.sep, 3)})
		cur = cur[:0]
	}
	for _, h := range hs {
		if len(cur) > 0 && h.at.Sub(cur[len(cur)-1].at) > s.c.MergeGap {
			flush()
		}
		cur = append(cur, h)
	}
	flush()
	return out
}

func rarityBasis(c Criteria) string {
	return fmt.Sprintf("The same members (the bodies and the deep-sky objects of the best frame) checked every %d minutes, %d years before and after, under the event rules: Sun below %g°, every member above %g°, all inside %.2f° × %.2f° at some rotator angle. Times up to %d hours apart count as one occurrence. Rare means no other occurrence within %d years either way. Moon and planets from NASA JPL's planetary tables.",
		c.StepMin, RecurrenceYears, c.SunBelow, c.MinAlt, c.UsableW, c.UsableH, int(c.MergeGap.Hours()), RareYears)
}

// judge places the event among the pairing's occurrences.
func judge(e Event, occ []Occurrence, from, to time.Time, c Criteria) *Rarity {
	r := &Rarity{Status: StatusReady, SearchedFrom: &from, SearchedTo: &to, Basis: rarityBasis(c), Method: MethodSearch}
	selfStart, selfEnd := e.Start, e.End
	for _, o := range occ {
		if o.Start.Before(selfEnd) && selfStart.Before(o.End) {
			selfStart, selfEnd = minTime(selfStart, o.Start), maxTime(selfEnd, o.End)
		}
	}
	for i, o := range occ {
		switch {
		case !o.End.After(selfStart):
			r.Previous = &occ[i]
		case !o.Start.Before(selfEnd) && r.Next == nil:
			r.Next = &occ[i]
		}
	}
	at := e.Best.At
	back, fore := years(at.Sub(from)), years(to.Sub(at))
	backKnown, foreKnown := r.Previous != nil, r.Next != nil
	if backKnown {
		back = years(at.Sub(r.Previous.Best))
		r.PreviousYears = ptr(round(back, 2))
	}
	if foreKnown {
		fore = years(r.Next.Best.Sub(at))
		r.NextYears = ptr(round(fore, 2))
	}
	switch {
	case backKnown && foreKnown:
		r.IntervalYears = ptr(round(math.Min(back, fore), 2))
	case backKnown && back <= fore:
		r.IntervalYears = ptr(round(back, 2))
	case foreKnown && fore <= back:
		r.IntervalYears = ptr(round(fore, 2))
	}
	r.Rare = back > RareYears && fore > RareYears
	return r
}

// Rarities searches each event's pairing over the decades around it,
// reusing earlier results whose key still matches.
func Rarities(e *ephem.Ephemeris, site sky.Site, c Criteria, events []Event, dsos []DSO, cached map[string]*Rarity, orbits []bodies.Orbit) {
	lo, hi := e.Range()
	type job struct {
		p        pairing
		from, to time.Time
		events   []int
	}
	jobs := map[string]*job{}
	var order []string
	for i := range events {
		if hasSmallBody(events[i]) {
			events[i].Rarity = orbitRarity(events[i], orbits)
			continue
		}
		p, reason := pairingOf(events[i], dsos)
		if reason != "" {
			events[i].Rarity = rarityUnavailable(reason)
			continue
		}
		from, to := recurrenceSpan(e, events[i].Best.At)
		key := rarityKey(p, events[i], site, c, lo, hi)
		if r, ok := cached[events[i].ID]; ok && r != nil && r.Key == key {
			events[i].Rarity = r
			continue
		}
		j, ok := jobs[p.key]
		if !ok {
			j = &job{p: p, from: from, to: to}
			jobs[p.key] = j
			order = append(order, p.key)
		}
		if from.Before(j.from) {
			j.from = from
		}
		if to.After(j.to) {
			j.to = to
		}
		j.events = append(j.events, i)
	}
	if len(order) == 0 {
		return
	}
	first, last := jobs[order[0]].from, jobs[order[0]].to
	for _, k := range order {
		first, last = minTime(first, jobs[k].from), maxTime(last, jobs[k].to)
	}
	sc := newScanner(e, site, c, first, last)
	need := map[ephem.Body]bool{}
	for _, k := range order {
		for _, b := range jobs[k].p.bodies {
			need[b] = true
		}
	}
	var wg sync.WaitGroup
	for b := range need {
		wg.Go(func() { _, _ = sc.track(b) })
	}
	wg.Wait()
	for _, k := range order {
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			hs, err := sc.scan(j.p, j.from, j.to)
			if err != nil {
				for _, i := range j.events {
					events[i].Rarity = rarityUnavailable("The recurrence search failed: " + err.Error())
				}
				return
			}
			occ := sc.occurrences(hs)
			for _, i := range j.events {
				from, to := recurrenceSpan(e, events[i].Best.At)
				r := judge(events[i], within(occ, from, to), from, to, c)
				p, _ := pairingOf(events[i], dsos)
				r.Key = rarityKey(p, events[i], site, c, lo, hi)
				events[i].Rarity = r
			}
		}(jobs[k])
	}
	wg.Wait()
}

func within(occ []Occurrence, from, to time.Time) []Occurrence {
	out := make([]Occurrence, 0, len(occ))
	for _, o := range occ {
		if !o.Best.Before(from) && o.Best.Before(to) {
			out = append(out, o)
		}
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func hasSmallBody(e Event) bool {
	return slices.ContainsFunc(e.Members, func(m Member) bool { return m.Kind == KindComet || m.Kind == KindAsteroid })
}

// orbitRarity judges a pairing with a comet or asteroid from its orbit: the
// same pairing cannot come back sooner than the body does, so a body on an
// open orbit or one longer than LifetimeYears makes the event once in a
// lifetime, and otherwise its period is the recurrence interval.
func orbitRarity(e Event, orbits []bodies.Orbit) *Rarity {
	var longest *bodies.Orbit
	var names []string
	open := false
	for _, m := range e.Members {
		if m.Kind != KindComet && m.Kind != KindAsteroid {
			continue
		}
		des := m.Name
		if m.Designation != nil {
			des = *m.Designation
		}
		i := slices.IndexFunc(orbits, func(o bodies.Orbit) bool { return bodies.SameDesignation(o.Designation, des) })
		if i < 0 {
			return rarityUnavailable("No orbit is known for " + des + ", so its return cannot be judged.")
		}
		o := orbits[i]
		if o.PeriodYears == nil {
			open = true
			names = append(names, fmt.Sprintf("%s is on an open orbit (e = %.4f, %s)", des, o.E, o.Source))
		} else {
			names = append(names, fmt.Sprintf("%s returns every %.1f years (e = %.4f, %s)", des, *o.PeriodYears, o.E, o.Source))
			if *o.PeriodYears > LifetimeYears {
				open = true
			}
			if longest == nil || *o.PeriodYears > *longest.PeriodYears {
				longest = &o
			}
		}
	}
	r := &Rarity{Status: StatusReady, Method: MethodOrbit}
	basis := "From the orbit, not a search: " + strings.Join(names, "; ") + fmt.Sprintf(". The same pairing cannot come back sooner than the body does. Rare means a period over %d years; over %d years, or an open orbit, counts as once in a lifetime.", RareYears, LifetimeYears)
	r.Basis = basis
	if open {
		r.Rare = true
		return r
	}
	r.IntervalYears = ptr(round(*longest.PeriodYears, 2))
	r.Rare = *longest.PeriodYears > RareYears
	return r
}
