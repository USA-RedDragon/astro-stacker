package skyevents

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
)

type recurrenceFixture struct {
	Site  sky.Site `json:"site"`
	Cases []struct {
		Name        string   `json:"name"`
		Bodies      []string `json:"bodies"`
		DSO         *string  `json:"dso"`
		Occurrences []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
			Best  time.Time `json:"best"`
			Sep   float64   `json:"sep"`
			Steps int       `json:"steps"`
		} `json:"occurrences"`
	} `json:"cases"`
}

const mars = "Mars"

func rarityInputs(t *testing.T) (*ephem.Ephemeris, []DSO, recurrenceFixture) {
	t.Helper()
	var fx recurrenceFixture
	e, err := ephem.Load()
	if err != nil {
		t.Fatal(err)
	}
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/skyfield-recurrence.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return e, EventDSOs(ix.All()), fx
}

func dsoIndex(t *testing.T, dsos []DSO, designation string) int {
	t.Helper()
	i := slices.IndexFunc(dsos, func(d DSO) bool { return d.Designation == designation })
	if i < 0 {
		t.Fatalf("no %s in the catalogue", designation)
	}
	return i
}

func search(t *testing.T, e *ephem.Ephemeris, site sky.Site, dsos []DSO, from, to time.Time) []Event {
	t.Helper()
	c := NewCriteria(3.32, 2.22)
	g, err := NewGrid(e, site, from, to, c)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]ephem.Body{ephem.Moon}, ephem.Planets()...)
	tracks := make([]Track, 0, len(all))
	for _, b := range all {
		tr, err := EphemerisTrack(e, b, g)
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, tr)
	}
	return Search(g, tracks, dsos, c)
}

func titled(t *testing.T, evs []Event, title string) Event {
	t.Helper()
	for _, e := range evs {
		if e.Title == title {
			return e
		}
	}
	t.Fatalf("no %s among %d events", title, len(evs))
	return Event{}
}

// The occurrences are a Skyfield brute-force scan of every 30-minute step
// from 1970 to 2090 for a synthetic site.
func TestRecurrenceMatchesSkyfield(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	c := NewCriteria(3.32, 2.22)
	from, to := time.Date(1970, 1, 3, 0, 0, 0, 0, time.UTC), time.Date(2090, 12, 30, 0, 0, 0, 0, time.UTC)
	sc := newScanner(e, fx.Site, c, from, to)
	for _, cs := range fx.Cases {
		var p pairing
		for _, n := range cs.Bodies {
			b, ok := ephemerisBody(n)
			if !ok {
				t.Fatal(n)
			}
			p.bodies = append(p.bodies, b)
		}
		if cs.DSO != nil {
			p.dsos = append(p.dsos, dsos[dsoIndex(t, dsos, *cs.DSO)])
		}
		lo, hi := cs.Occurrences[0].Start.Add(-400*24*time.Hour), cs.Occurrences[len(cs.Occurrences)-1].End.Add(400*24*time.Hour)
		hs, err := sc.scan(p, maxTime(lo, from), minTime(hi, to))
		if err != nil {
			t.Fatal(err)
		}
		got := sc.occurrences(hs)
		matched := 0
		for _, want := range cs.Occurrences {
			i := slices.IndexFunc(got, func(o Occurrence) bool { return o.Start.Before(want.End) && want.Start.Before(o.End) })
			if i < 0 {
				if want.Steps > 1 {
					t.Errorf("%s: no occurrence near %s (%d steps)", cs.Name, want.Best, want.Steps)
				}
				continue
			}
			matched++
			if d := got[i].Best.Sub(want.Best); want.Steps > 2 && math.Abs(d.Hours()) > 24 {
				t.Errorf("%s: best %s, Skyfield %s", cs.Name, got[i].Best, want.Best)
			}
			if math.Abs(got[i].Separation-want.Sep) > 0.02 {
				t.Errorf("%s %s: closest %.3f°, Skyfield %.3f°", cs.Name, want.Best, got[i].Separation, want.Sep)
			}
		}
		if len(got) != matched {
			for _, o := range got {
				if !slices.ContainsFunc(cs.Occurrences, func(w struct {
					Start time.Time `json:"start"`
					End   time.Time `json:"end"`
					Best  time.Time `json:"best"`
					Sep   float64   `json:"sep"`
					Steps int       `json:"steps"`
				}) bool {
					return o.Start.Before(w.End) && w.Start.Before(o.End)
				}) && o.End.Sub(o.Start) > c.Step {
					t.Errorf("%s: extra occurrence %+v", cs.Name, o)
				}
			}
		}
	}
}

func TestRarityOfACommonAndARarePairing(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	cases := []struct {
		title      string
		from, to   time.Time
		rare       bool
		prev, next time.Time
	}{
		{"Mars with M 44 Beehive", time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), false,
			time.Date(2025, 5, 5, 6, 0, 0, 0, time.UTC), time.Date(2028, 9, 17, 10, 0, 0, 0, time.UTC)},
		{"Jupiter with Saturn", time.Date(2020, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2021, 1, 15, 0, 0, 0, 0, time.UTC), true,
			time.Date(1981, 8, 8, 2, 30, 0, 0, time.UTC), time.Date(2040, 11, 12, 12, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		evs := search(t, e, fx.Site, dsos, tc.from, tc.to)
		Rarities(e, fx.Site, NewCriteria(3.32, 2.22), evs, dsos, nil, nil)
		ev := titled(t, evs, tc.title)
		r := ev.Rarity
		if r == nil || r.Status != StatusReady || r.Previous == nil || r.Next == nil {
			t.Fatalf("%s: %+v", tc.title, r)
		}
		if r.Rare != tc.rare || ev.IsRare() != tc.rare {
			t.Errorf("%s: rare %v, want %v", tc.title, r.Rare, tc.rare)
		}
		if d := r.Previous.Best.Sub(tc.prev); math.Abs(d.Hours()) > 24 {
			t.Errorf("%s: previous %s, Skyfield %s", tc.title, r.Previous.Best, tc.prev)
		}
		if d := r.Next.Best.Sub(tc.next); math.Abs(d.Hours()) > 24 {
			t.Errorf("%s: next %s, Skyfield %s", tc.title, r.Next.Best, tc.next)
		}
		wantPrev, wantNext := years(ev.Best.At.Sub(tc.prev)), years(tc.next.Sub(ev.Best.At))
		if math.Abs(*r.PreviousYears-wantPrev) > 0.01 || math.Abs(*r.NextYears-wantNext) > 0.01 || math.Abs(*r.IntervalYears-math.Min(wantPrev, wantNext)) > 0.01 {
			t.Errorf("%s: %.2f / %.2f / %.2f years, Skyfield %.2f / %.2f", tc.title, *r.PreviousYears, *r.NextYears, *r.IntervalYears, wantPrev, wantNext)
		}
	}
}

func TestDailyPassMissesNoStep(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	c := NewCriteria(3.32, 2.22)
	from, to := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	sc := newScanner(e, fx.Site, c, from, to)
	for _, p := range []pairing{
		{bodies: []ephem.Body{ephem.Moon, ephem.Jupiter}},
		{bodies: []ephem.Body{ephem.Moon, ephem.Venus}},
		{bodies: []ephem.Body{ephem.Venus, ephem.Saturn}},
		{bodies: []ephem.Body{ephem.Venus}, dsos: []DSO{dsos[dsoIndex(t, dsos, "M 9")]}},
		{bodies: []ephem.Body{ephem.Mars}, dsos: []DSO{dsos[dsoIndex(t, dsos, "M 44")]}},
	} {
		hs, err := sc.scan(p, from, to)
		if err != nil {
			t.Fatal(err)
		}
		var brute []time.Time
		for at := from; at.Before(to); at = at.Add(c.Step) {
			if _, ok, err := sc.fits(p, at); err != nil {
				t.Fatal(err)
			} else if ok {
				brute = append(brute, at)
			}
		}
		if len(brute) == 0 || len(hs) != len(brute) {
			t.Fatalf("%v: %d steps from the daily pass, %d by brute force", p.bodies, len(hs), len(brute))
		}
		for i := range hs {
			if !hs[i].at.Equal(brute[i]) {
				t.Fatalf("%v: step %d is %s, brute force %s", p.bodies, i, hs[i].at, brute[i])
			}
		}
	}
}

func TestJudge(t *testing.T) {
	t.Parallel()
	c := NewCriteria(3.32, 2.22)
	at := time.Date(2030, 6, 1, 4, 0, 0, 0, time.UTC)
	ev := Event{Start: at.Add(-48 * time.Hour), End: at.Add(48 * time.Hour), Best: Best{At: at}}
	from, to := at.AddDate(-50, 0, 0), at.AddDate(50, 0, 0)
	occ := func(y int) Occurrence {
		b := at.AddDate(y, 0, 0)
		return Occurrence{Start: b.Add(-time.Hour), End: b.Add(time.Hour), Best: b}
	}
	r := judge(ev, nil, from, to, c)
	if !r.Rare || r.IntervalYears != nil || r.Previous != nil || r.Next != nil {
		t.Errorf("no recurrence: %+v", r)
	}
	r = judge(ev, []Occurrence{occ(-30), occ(0), occ(12)}, from, to, c)
	if !r.Rare || r.Previous == nil || r.Next == nil || math.Round(*r.IntervalYears) != 12 || math.Round(*r.PreviousYears) != 30 {
		t.Errorf("12 and 30 years: %+v", r)
	}
	r = judge(ev, []Occurrence{occ(-2), occ(0), occ(9)}, from, to, c)
	if r.Rare || math.Round(*r.IntervalYears) != 2 {
		t.Errorf("2 years: %+v", r)
	}
	r = judge(ev, []Occurrence{occ(-20)}, from, at.AddDate(5, 0, 0), c)
	if r.Rare || r.IntervalYears != nil || r.Next != nil {
		t.Errorf("search ends 5 years ahead: %+v", r)
	}
	r = judge(ev, []Occurrence{occ(-20)}, from, at.AddDate(30, 0, 0), c)
	if !r.Rare || math.Round(*r.IntervalYears) != 20 {
		t.Errorf("none within 30 years ahead: %+v", r)
	}
}

func TestRaritiesReuseAndComets(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	c := NewCriteria(3.32, 2.22)
	evs := search(t, e, fx.Site, dsos, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	Rarities(e, fx.Site, c, evs, dsos, nil, nil)
	cached := map[string]*Rarity{}
	for _, ev := range evs {
		r := *ev.Rarity
		r.Basis = "cached"
		cached[ev.ID] = &r
	}
	again := search(t, e, fx.Site, dsos, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	stale := *cached[again[0].ID]
	stale.Key = "old"
	cached[again[0].ID] = &stale
	Rarities(e, fx.Site, c, again, dsos, cached, nil)
	for i, ev := range again {
		if (i == 0) == (ev.Rarity.Basis == "cached") {
			t.Errorf("%s: basis %q", ev.ID, ev.Rarity.Basis)
		}
	}
	comet := Event{ID: "c", Members: []Member{{Name: "C/2025 R2", Kind: KindComet}, {Name: mars, Kind: KindPlanet}}}
	one := []Event{comet}
	Rarities(e, fx.Site, c, one, dsos, nil, nil)
	if one[0].Rarity == nil || one[0].Rarity.Status != StatusUnavailable || one[0].Rarity.Reason == nil || one[0].IsRare() {
		t.Errorf("comet: %+v", one[0].Rarity)
	}
}

func TestScannerCoversTheWholeSpan(t *testing.T) {
	t.Parallel()
	e, _, _ := rarityInputs(t)
	c := NewCriteria(3.32, 2.22)
	from, to := time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2030, 3, 20, 0, 0, 0, 0, time.UTC)
	for _, lon := range []float64{-179, -100, 0, 100, 150, 179} {
		for _, shift := range []time.Duration{0, 5 * time.Hour, 13 * time.Hour, 23 * time.Hour} {
			sc := newScanner(e, sky.Site{Latitude: 30, Longitude: lon}, c, from, to.Add(shift))
			half := CoarseStep / 2
			if sc.day(0).Add(-half).After(from) || sc.day(sc.days-1).Add(half).Before(to.Add(shift)) {
				t.Errorf("lon %g, end +%s: days %s to %s", lon, shift, sc.day(0).Add(-half), sc.day(sc.days-1).Add(half))
			}
		}
	}
}

func TestRarityFromTheOrbit(t *testing.T) {
	t.Parallel()
	e, _, _ := rarityInputs(t)
	lines := []string{
		"0161P         2026 11 27.1781  1.265111  0.836180   47.0650    1.4736   95.7919  20261011   8.5  6.0  161P/Hartley-IRAS                                        MPEC 2026-TI5",
		"0002P         2027 02 10.2277  0.338605  0.847318  187.2872  334.0197   11.3480  20261011  14.4  4.0  2P/Encke                                                 MPC xxxxx",
		"    CK25R020  2025 09 12.8052  0.504283  0.994157  308.3755  335.3139    4.4729  20261011  12.4  4.0  C/2025 R2 (SWAN)                                         MPEC 2026-N12",
	}
	orbits := make([]bodies.Orbit, 0, len(lines)+1)
	for _, l := range lines {
		el, err := bodies.ParseCometLine(l)
		if err != nil {
			t.Fatal(err)
		}
		orbits = append(orbits, bodies.OrbitOf(el, "MPC comet elements"))
	}
	orbits = append(orbits, bodies.Orbit{Designation: "C/2026 A1", E: 1.0003, Source: "JPL SBDB"})
	cases := []struct {
		des      string
		rare     bool
		interval float64
	}{
		{"161P", true, 21.4},
		{"2P", false, 3.3},
		{"C/2025 R2", true, 0},
		{"C/2026 A1", true, 0},
	}
	for _, tc := range cases {
		d := tc.des
		ev := Event{ID: d, Members: []Member{{Name: mars, Kind: KindPlanet}, {Name: d, Kind: KindComet, Designation: &d}}}
		evs := []Event{ev}
		Rarities(e, sky.Site{}, NewCriteria(3.32, 2.22), evs, nil, nil, orbits)
		r := evs[0].Rarity
		if r == nil || r.Status != StatusReady || r.Method != MethodOrbit || r.Rare != tc.rare || !strings.Contains(r.Basis, "orbit") {
			t.Fatalf("%s: %+v", d, r)
		}
		switch {
		case tc.interval == 0 && r.IntervalYears != nil:
			t.Errorf("%s: interval %v, want none", d, *r.IntervalYears)
		case tc.interval != 0 && (r.IntervalYears == nil || math.Abs(*r.IntervalYears-tc.interval) > 0.1):
			t.Errorf("%s: interval %v, want %v", d, r.IntervalYears, tc.interval)
		}
	}
	unknown := "C/2099 Z9"
	evs := []Event{{ID: "u", Members: []Member{{Name: unknown, Kind: KindComet, Designation: &unknown}, {Name: mars, Kind: KindPlanet}}}}
	Rarities(e, sky.Site{}, NewCriteria(3.32, 2.22), evs, nil, nil, orbits)
	if evs[0].Rarity.Status != StatusUnavailable {
		t.Errorf("unknown orbit: %+v", evs[0].Rarity)
	}
}
