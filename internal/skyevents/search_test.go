package skyevents_test

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
)

const mars = "Mars"

type reference struct {
	Body   string    `json:"body"`
	Others []string  `json:"others"`
	First  string    `json:"first"`
	Last   string    `json:"last"`
	Best   time.Time `json:"best"`
	Sep    float64   `json:"sep"`
	Nights int       `json:"nights"`
}

type fixture struct {
	Site   sky.Site    `json:"site"`
	Events []reference `json:"events"`
}

type searched struct {
	fx     fixture
	events []skyevents.Event
	err    error
}

func runSearch() searched {
	var out searched
	raw, err := os.ReadFile("testdata/skyfield-events.json")
	if err != nil {
		out.err = err
		return out
	}
	if out.err = json.Unmarshal(raw, &out.fx); out.err != nil {
		return out
	}
	e, err := ephem.Load()
	if err != nil {
		out.err = err
		return out
	}
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		out.err = err
		return out
	}
	c := skyevents.NewCriteria(3.32, 2.22)
	from := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	g, err := skyevents.NewGrid(e, out.fx.Site, from, from.Add(183*24*time.Hour), c)
	if err != nil {
		out.err = err
		return out
	}
	var tracks []skyevents.Track
	for _, b := range append([]ephem.Body{ephem.Moon}, ephem.Planets()...) {
		tr, err := skyevents.EphemerisTrack(e, b, g)
		if err != nil {
			out.err = err
			return out
		}
		tracks = append(tracks, tr)
	}
	out.events = skyevents.Search(g, tracks, skyevents.EventDSOs(ix.All()), c)
	return out
}

type memo struct {
	once sync.Once
	s    searched
}

func (m *memo) get(t *testing.T) ([]skyevents.Event, fixture) {
	t.Helper()
	m.once.Do(func() { m.s = runSearch() })
	if m.s.err != nil {
		t.Fatal(m.s.err)
	}
	return m.s.events, m.s.fx
}

func names(e skyevents.Event) []string {
	out := make([]string, 0, len(e.Members))
	for _, m := range e.Members {
		out = append(out, m.Name)
	}
	return out
}

func find(events []skyevents.Event, members []string, near time.Time) *skyevents.Event {
	var best *skyevents.Event
	for i, e := range events {
		got := names(e)
		if len(got) != len(members) {
			continue
		}
		ok := true
		for _, m := range members {
			ok = ok && slices.Contains(got, m)
		}
		d := math.Abs(e.Best.At.Sub(near).Hours())
		if ok && d < 24*30 && (best == nil || d < math.Abs(best.Best.At.Sub(near).Hours())) {
			best = &events[i]
		}
	}
	return best
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSearch(t *testing.T) {
	t.Parallel()
	m := &memo{}
	t.Run("matches the Skyfield reference", func(t *testing.T) {
		t.Parallel()
		matchesReference(t, m)
	})
	t.Run("finds the planned highlights", func(t *testing.T) {
		t.Parallel()
		highlights(t, m)
	})
	t.Run("splits groups that cannot share a frame", func(t *testing.T) {
		t.Parallel()
		splitsGroups(t, m)
	})
	t.Run("frames hold every object at their rotation", func(t *testing.T) {
		t.Parallel()
		framesHold(t, m)
	})
}

// The reference is the Skyfield prototype the mockup was built from, run at
// the same synthetic site. Its Venus groups of M 23, M 20, M 21 and of M 25,
// M 22 were joined by date without checking that they share a frame; they do
// not, so they are left out here and checked in splitsGroups.
func matchesReference(t *testing.T, m *memo) {
	events, fx := m.get(t)
	checked := 0
	for _, r := range fx.Events {
		if r.Body == "Venus" && len(r.Others) > 1 {
			continue
		}
		members := append([]string{r.Body}, r.Others...)
		e := find(events, members, r.Best)
		if e == nil {
			t.Errorf("no event for %v near %s", members, r.Best)
			continue
		}
		checked++
		if d := math.Abs(e.Best.At.Sub(r.Best).Hours()); d > 3 {
			t.Errorf("%s: best %s, reference %s", e.Title, e.Best.At, r.Best)
		}
		if d := math.Abs(e.Best.Separation - r.Sep); d > 0.05 {
			t.Errorf("%s: separation %.3f, reference %.2f", e.Title, e.Best.Separation, r.Sep)
		}
		if d := math.Abs(e.Start.Sub(day(t, r.First)).Hours()); d > 36 {
			t.Errorf("%s: starts %s, reference %s", e.Title, e.Start, r.First)
		}
		if d := math.Abs(e.End.Sub(day(t, r.Last).Add(24 * time.Hour)).Hours()); d > 36 {
			t.Errorf("%s: ends %s, reference %s", e.Title, e.End, r.Last)
		}
	}
	if checked != 9 {
		t.Errorf("checked %d reference events, want 9", checked)
	}
}

func highlights(t *testing.T, m *memo) {
	events, _ := m.get(t)
	m44 := find(events, []string{mars, "M 44 Beehive"}, time.Date(2026, 10, 13, 8, 30, 0, 0, time.UTC))
	if m44 == nil || math.Abs(m44.Best.Separation-1.0) > 0.05 || m44.Kind != skyevents.EventBodyDSO || m44.MovingCenter {
		t.Fatalf("Mars and M 44: %+v", m44)
	}
	mj := find(events, []string{mars, "Jupiter"}, time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))
	if mj == nil || math.Abs(mj.Best.Separation-1.2) > 0.05 || mj.Nights < 14 || !mj.MovingCenter || mj.Kind != skyevents.EventBodyBody {
		t.Fatalf("Mars and Jupiter: %+v", mj)
	}
	if len(mj.NightWindows) != mj.Nights || mj.NightWindows[0].CenterRA == mj.NightWindows[len(mj.NightWindows)-1].CenterRA {
		t.Errorf("Mars and Jupiter night centres do not move: %+v", mj.NightWindows)
	}
	leo := find(events, []string{mars, "M 95", "M 96", "M 105"}, time.Date(2027, 1, 25, 0, 0, 0, 0, time.UTC))
	if leo == nil {
		t.Fatal("Mars with M 95, M 96 and M 105 is not one event")
	}
	if leo.Title != "Mars with M 95, M 96, M 105" || leo.Nights < 50 || leo.Start.After(time.Date(2026, 12, 13, 0, 0, 0, 0, time.UTC)) ||
		leo.End.Before(time.Date(2027, 2, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Leo triplet window: %s %s to %s, %d nights", leo.Title, leo.Start, leo.End, leo.Nights)
	}
	if len(leo.Frame.Objects) != 4 {
		t.Errorf("the best moment frame holds %d objects, want Mars and all three galaxies", len(leo.Frame.Objects))
	}
}

func splitsGroups(t *testing.T, m *memo) {
	events, _ := m.get(t)
	for _, e := range events {
		got := names(e)
		hasM23 := slices.Contains(got, "M 23")
		hasM20 := slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "M 20 ") })
		if hasM23 && hasM20 {
			t.Errorf("%s holds M 23 and M 20, which are 4° apart", e.Title)
		}
	}
	if find(events, []string{"Venus", "M 20 Trifid Nebula", "M 21"}, time.Date(2027, 2, 4, 12, 0, 0, 0, time.UTC)) == nil {
		t.Error("Venus with M 20 and M 21 is missing")
	}
}

func framesHold(t *testing.T, m *memo) {
	events, _ := m.get(t)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		seen[e.ID] = true
		f := e.Frame
		centre := mosaics.Point{RA: f.CenterRA, Dec: f.CenterDec}
		for _, o := range f.Objects {
			r := o.Diameter / 2
			if math.Abs(o.X)+r > f.Width/2 || math.Abs(o.Y)+r > f.Height/2 {
				t.Errorf("%s: %s at (%.2f, %.2f) r %.2f is outside the %.2f x %.2f frame", e.Title, o.Name, o.X, o.Y, r, f.Width, f.Height)
			}
			back := mosaics.Offset(centre, f.Rotation, o.X, o.Y)
			fwd := mosaics.Offset(centre, 0, o.East, o.North)
			if s := catalog.Separation(back.RA, back.Dec, fwd.RA, fwd.Dec); s > 0.01 {
				t.Errorf("%s: %s frame axes disagree with east/north by %.3f°", e.Title, o.Name, s)
			}
		}
		if e.Hours <= 0 || e.Nights != len(e.NightWindows) || !e.End.After(e.Start) || e.ID == "" || e.Priority != skyevents.PriorityNormal {
			t.Errorf("%s: %+v", e.Title, e)
		}
	}
}
