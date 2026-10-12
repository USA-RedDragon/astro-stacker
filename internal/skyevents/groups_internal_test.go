package skyevents

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
)

const (
	moon    = "Moon"
	jupiter = "Jupiter"
	saturn  = "Saturn"
	venus   = "Venus"
)

func bruteSlack(ds []disc, w, h float64) float64 {
	p := project(ds)
	best := math.Inf(-1)
	for k := range 90000 {
		s, _, _ := p.box(float64(k)*0.002, w, h)
		best = math.Max(best, s)
	}
	return best
}

func TestFitRectMatchesABruteForceSweep(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(7, 11))
	w, h := 3.004, 1.901
	for n := range 300 {
		ds := make([]disc, 2+n%3)
		for i := range ds {
			ds[i] = disc{ra: 120 + r.Float64()*3.4, dec: 20 + r.Float64()*2.6, radius: []float64{0, 0.005, 0.27}[r.IntN(3)]}
		}
		p := project(ds)
		f, ok := fitRect(ds, w, h, math.Hypot(w, h))
		brute := bruteSlack(ds, w, h)
		if ok {
			if s, _, _ := p.box(f.rotation, w, h); s < 0 || math.Abs(s-f.slack) > 1e-9 {
				t.Fatalf("case %d: fit at %.3f° leaves %.4f°, reported %.4f°", n, f.rotation, s, f.slack)
			}
			if f.slack < brute-0.003 {
				t.Errorf("case %d: fit leaves %.4f°, the sweep finds %.4f°", n, f.slack, brute)
			}
		} else if brute > 1e-4 {
			t.Errorf("case %d: no fit, the sweep fits with %.4f° to spare", n, brute)
		}
	}
}

func TestFitRectFindsANarrowRotation(t *testing.T) {
	t.Parallel()
	w, h := 3.004, 1.901
	centre := mosaics.Point{RA: 150, Dec: 10}
	want := 37.4
	l := w - 0.01
	a, b, c := mosaics.Offset(centre, want, -l/2, 0), mosaics.Offset(centre, want, l/2, 0), mosaics.Offset(centre, want, 0, 0.997*h)
	ds := []disc{{ra: a.RA, dec: a.Dec}, {ra: b.RA, dec: b.Dec}, {ra: c.RA, dec: c.Dec}}
	p := project(ds)
	for k := range 180 {
		if s, _, _ := p.box(float64(k), w, h); s >= 0 {
			t.Fatalf("the 1° sweep already fits at %d°", k)
		}
	}
	f, ok := fitFrame(ds, w, h)
	if !ok || math.Abs(f.rotation-want) > 0.3 {
		t.Fatalf("fit %+v %v, want a rotation near %.1f°", f, ok, want)
	}
}

func membersOf(e Event) []string {
	out := make([]string, 0, len(e.Members))
	for _, m := range e.Members {
		out = append(out, m.Name)
	}
	return out
}

func eventOf(evs []Event, kind string, members ...string) *Event {
	for i, e := range evs {
		if e.Kind == kind && slices.Equal(membersOf(e), members) {
			return &evs[i]
		}
	}
	return nil
}

// The expected times and spreads are a Skyfield brute-force scan of every
// 30-minute step at the same synthetic site, fitting each group in the
// usable field or two panels at 0.05° rotator steps.
func TestGroupsMatchSkyfield(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	cases := []struct {
		kind    string
		members []string
		from    time.Time
		best    time.Time
		spread  float64
		steps   int
		cols    int
	}{
		{EventBodyBody, []string{moon, saturn, "Neptune"}, time.Date(2025, 7, 14, 0, 0, 0, 0, time.UTC), time.Date(2025, 7, 16, 6, 30, 0, 0, time.UTC), 2.554, 4, 0},
		{EventBodyBody, []string{moon, mars, jupiter}, time.Date(2020, 3, 16, 0, 0, 0, 0, time.UTC), time.Date(2020, 3, 18, 11, 30, 0, 0, time.UTC), 2.501, 1, 0},
		{EventMosaic2, []string{moon, mars, jupiter}, time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 30, 8, 30, 0, 0, time.UTC), 5.151, 5, 2},
		{EventMosaic2, []string{moon, venus, mars}, time.Date(2028, 9, 13, 0, 0, 0, 0, time.UTC), time.Date(2028, 9, 15, 10, 30, 0, 0, time.UTC), 4.113, 3, 1},
	}
	for _, tc := range cases {
		evs := search(t, e, fx.Site, dsos, tc.from, tc.from.Add(96*time.Hour))
		ev := eventOf(evs, tc.kind, tc.members...)
		if ev == nil {
			t.Errorf("no %s event for %v", tc.kind, tc.members)
			continue
		}
		if !ev.Best.At.Equal(tc.best) || math.Abs(ev.Best.Separation-tc.spread) > 0.01 || ev.Hours != float64(tc.steps)/2 {
			t.Errorf("%s: best %s at %.3f° over %.1f h, Skyfield %s at %.3f° over %d steps", ev.Title, ev.Best.At, ev.Best.Separation, ev.Hours, tc.best, tc.spread, tc.steps)
		}
		m := ev.Frame.Mosaic
		if (m == nil) != (tc.cols == 0) || (m != nil && (m.Cols != tc.cols || len(m.Panels) != 2 || m.Overlap != MosaicOverlap)) {
			t.Errorf("%s: mosaic %+v", ev.Title, m)
		}
		if ev.ID == "" || !ev.MovingCenter {
			t.Errorf("%s: %+v", ev.Title, ev)
		}
	}
}

func TestATripleReplacesItsPairs(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	c := NewCriteria(3.32, 2.22)
	at := time.Date(2025, 7, 16, 7, 0, 0, 0, time.UTC)
	g, err := NewGrid(e, fx.Site, at, at.Add(time.Minute), c)
	if err != nil {
		t.Fatal(err)
	}
	bs := append([]ephem.Body{ephem.Moon}, ephem.Planets()...)
	tracks := make([]Track, 0, len(bs))
	for _, b := range bs {
		tr, err := EphemerisTrack(e, b, g)
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, tr)
	}
	s := &step{g: g, i: 0, tracks: tracks, dsos: dsos, c: c, alt: map[int]bool{}}
	for b, tr := range tracks {
		if smp := tr.Samples[0]; smp.OK && smp.Alt > c.MinAlt {
			s.up = append(s.up, b)
		}
	}
	_, all, top := s.groups(c.single())
	var bodyGroups [][]string
	for _, gr := range top {
		if len(gr.dsos) == 0 {
			var n []string
			for _, b := range orderBodies(tracks, gr.bodies) {
				n = append(n, tracks[b].Name)
			}
			bodyGroups = append(bodyGroups, n)
		}
	}
	if !slices.EqualFunc(bodyGroups, [][]string{{moon, saturn, "Neptune"}}, slices.Equal[[]string]) {
		t.Errorf("groups no larger group holds: %v", bodyGroups)
	}
	pairs := 0
	for _, gr := range all {
		if len(gr.bodies) == 2 && len(gr.dsos) == 0 {
			pairs++
		}
	}
	if pairs != 3 {
		t.Errorf("%d pairs fit, want the triple's 3", pairs)
	}
}

func TestEventsDoNotRepeatOrOverlap(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	evs := search(t, e, fx.Site, dsos, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2028, 11, 1, 0, 0, 0, 0, time.UTC))
	seen := map[string]bool{}
	for i, a := range evs {
		if seen[a.ID] {
			t.Errorf("duplicate id %s", a.ID)
		}
		seen[a.ID] = true
		ka, oa := memberKeys(a)
		for _, b := range evs[i+1:] {
			kb, ob := memberKeys(b)
			if ka != kb || !slices.Equal(oa, ob) {
				continue
			}
			near := a.Start.Before(b.End.Add(MergeGap)) && b.Start.Before(a.End.Add(MergeGap))
			if a.Kind == b.Kind && a.Start.Before(b.End) && b.Start.Before(a.End) {
				t.Errorf("%s and %s overlap", a.ID, b.ID)
			}
			if (a.Kind == EventMosaic2) != (b.Kind == EventMosaic2) && near {
				t.Errorf("%s and %s are the same members in one frame and in a mosaic", a.ID, b.ID)
			}
		}
	}
}

func TestRarityOfGroups(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	cases := []struct {
		from       time.Time
		members    []string
		rare       bool
		prev, next time.Time
	}{
		{time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC), []string{moon, mars, jupiter}, false, time.Date(2020, 3, 18, 11, 30, 0, 0, time.UTC), time.Date(2033, 11, 29, 1, 0, 0, 0, time.UTC)},
		{time.Date(2028, 9, 13, 0, 0, 0, 0, time.UTC), []string{moon, venus, mars}, true, time.Date(2015, 11, 7, 11, 0, 0, 0, time.UTC), time.Time{}},
	}
	for _, tc := range cases {
		evs := search(t, e, fx.Site, dsos, tc.from, tc.from.Add(96*time.Hour))
		Rarities(e, fx.Site, NewCriteria(3.32, 2.22), evs, dsos, nil, nil)
		ev := eventOf(evs, EventMosaic2, tc.members...)
		if ev == nil {
			t.Fatalf("no mosaic of %v", tc.members)
		}
		r := ev.Rarity
		if r == nil || r.Status != StatusReady || r.Rare != tc.rare || r.Previous == nil || !r.Previous.Best.Equal(tc.prev) {
			t.Fatalf("%s: %+v", ev.Title, r)
		}
		if tc.next.IsZero() != (r.Next == nil) || (r.Next != nil && !r.Next.Best.Equal(tc.next)) {
			t.Errorf("%s: next %+v, Skyfield %s", ev.Title, r.Next, tc.next)
		}
		p, _ := pairingOf(*ev, dsos)
		if !p.mosaic || len(p.bodies) != 3 {
			t.Errorf("%s: pairing %+v", ev.Title, p)
		}
	}
}

func TestOnlyLargeOrRareMosaicsAreListed(t *testing.T) {
	t.Parallel()
	rare := &Rarity{Status: StatusReady, Rare: true}
	common := &Rarity{Status: StatusReady}
	two := []Member{{Name: moon, Kind: KindMoon}, {Name: mars, Kind: KindPlanet}}
	three := []Member{{Name: moon, Kind: KindMoon}, {Name: mars, Kind: KindPlanet}, {Name: jupiter, Kind: KindPlanet}}
	withDSOs := []Member{{Name: venus, Kind: KindPlanet}, {Name: "M 8", Kind: KindDSO}, {Name: "M 20", Kind: KindDSO}}
	evs := []Event{
		{ID: "planet-dsos-mosaic", Kind: EventMosaic2, Members: withDSOs, Rarity: common},
		{ID: "rare-planet-dsos-mosaic", Kind: EventMosaic2, Members: withDSOs, Rarity: rare},
		{ID: "pair", Kind: EventBodyBody, Members: two, Rarity: common},
		{ID: "pair-mosaic", Kind: EventMosaic2, Members: two, Rarity: common},
		{ID: "pair-mosaic-unknown", Kind: EventMosaic2, Members: two},
		{ID: "rare-pair-mosaic", Kind: EventMosaic2, Members: two, Rarity: rare},
		{ID: "triple-mosaic", Kind: EventMosaic2, Members: three, Rarity: common},
	}
	kept := WorthAMosaic(evs)
	got := make([]string, 0, len(kept))
	for _, e := range kept {
		got = append(got, e.ID)
	}
	if !slices.Equal(got, []string{"rare-planet-dsos-mosaic", "pair", "rare-pair-mosaic", "triple-mosaic"}) {
		t.Errorf("kept %v", got)
	}
}
