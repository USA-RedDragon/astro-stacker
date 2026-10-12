package skyevents

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/ephem"
	"github.com/USA-RedDragon/astro-stacker/internal/mosaics"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const cmdID = "cmd"

type oneRecord struct {
	schedcmd.Log
	rec schedcmd.Record
}

func (l oneRecord) Get(_ context.Context, id string) (schedcmd.Record, error) {
	if id != l.rec.ID {
		return schedcmd.Record{}, schedcmd.ErrNotFound
	}
	return l.rec, nil
}

func mosaicPayload(t *testing.T, ev Event, rotation float64) json.RawMessage {
	t.Helper()
	m := ev.Frame.Mosaic
	p := schedcmd.ProjectCreatePayload{Project: schedcmd.NewProject{GUID: "project"},
		Mosaic: &schedcmd.MosaicPlan{Rotation: rotation, Overlap: m.Overlap, Cols: m.Cols, Rows: m.Rows}}
	centre := mosaics.Point{RA: ev.Frame.CenterRA, Dec: ev.Frame.CenterDec}
	for _, pn := range m.Panels {
		at := mosaics.Offset(centre, rotation, pn.X, pn.Y)
		p.Targets = append(p.Targets, schedcmd.NewTarget{GUID: fmt.Sprintf("panel-%d", pn.N), Name: panelName(ev.Title, pn.N), RAHours: at.RA / 15, Dec: at.Dec, Rotation: rotation})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCentresFollowTheSentFrame(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	evs := search(t, e, fx.Site, dsos, time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC))
	ev := eventOf(evs, EventMosaic2, moon, mars, jupiter)
	if ev == nil {
		t.Fatal("no mosaic of the Moon, Mars and Jupiter")
	}
	c := NewCriteria(3.32, 2.22)
	for _, rotation := range []float64{ev.Frame.Rotation, ev.Frame.Rotation + 2} {
		svc := &Service{Commands: &schedcmd.Service{Log: oneRecord{rec: schedcmd.Record{ID: cmdID, Payload: mosaicPayload(t, *ev, rotation)}}}}
		svc.site, svc.last = fx.Site, &Result{Criteria: &c}
		start, end := ev.Start, ev.End
		choice := app.SkyEventChoice{EventID: "renamed", Bodies: "body:jupiter,body:mars,body:moon", CommandID: cmdID, Designation: "event:x",
			Moving: true, WindowStart: &start, WindowEnd: &end}
		f := svc.sent(context.Background(), choice)
		if f.rotation != rotation || len(f.targets) != 2 || math.Abs(f.targets[0].x-ev.Frame.Mosaic.Panels[0].X) > 0.001 || math.Abs(f.w-6.142) > 0.001 {
			t.Fatalf("sent frame %+v", f)
		}
		rows := svc.centreRows(context.Background(), choice, time.Now())
		if len(rows) == 0 || len(rows)%2 != 0 {
			t.Fatalf("rotation %.1f: %d rows", rotation, len(rows))
		}
		for i := 0; i < len(rows); i += 2 {
			a, b := rows[i], rows[i+1]
			o := ephem.NewObserver(fx.Site, a.At)
			ds := make([]disc, 0, 3)
			for _, body := range []ephem.Body{ephem.Moon, ephem.Mars, ephem.Jupiter} {
				p, err := e.Observe(body, o)
				if err != nil {
					t.Fatal(err)
				}
				ds = append(ds, disc{ra: p.RA, dec: p.Dec, radius: p.Diameter / 2})
			}
			p := project(ds)
			if slack, _, _ := p.box(rotation, f.w, f.h); slack < 0 || a.TargetGUID == b.TargetGUID || !a.At.Equal(b.At) {
				t.Fatalf("rotation %.1f: rows at %s leave %.3f°", rotation, a.At, slack)
			}
			mid := centreAt(ds, rotation)
			m := mosaics.Offset(mid, rotation, ev.Frame.Mosaic.Panels[0].X, 0)
			if d := catalog.Separation(m.RA, m.Dec, a.RA, a.Dec); d > 0.002 {
				t.Fatalf("rotation %.1f: panel 1 is %.4f° from where the sent frame puts it", rotation, d)
			}
		}
	}
}

func TestEventHoursCountEachStepOnceAndHoldAtTheRotation(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	c := NewCriteria(3.32, 2.22)
	g, err := NewGrid(e, fx.Site, from, from.AddDate(1, 0, 0), c)
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
	for _, ev := range Search(g, tracks, dsos, c) {
		span := 0.0
		for _, n := range ev.NightWindows {
			span += n.End.Sub(n.Start).Hours()
		}
		if ev.Hours > span+0.01 {
			t.Errorf("%s: %.1f h in frame but the nights span %.1f h", ev.ID, ev.Hours, span)
		}
		if !ev.MovingCenter {
			continue
		}
		sh := c.single()[0]
		if m := ev.Frame.Mosaic; m != nil {
			sh = c.mosaic()[m.Rows-1]
		}
		for _, n := range ev.NightWindows {
			for i, at := range g.Times {
				if !at.Equal(n.Start) && !at.Equal(n.End.Add(-c.Step)) {
					continue
				}
				ds := make([]disc, 0, len(ev.bodies))
				for _, b := range ev.bodies {
					ds = append(ds, bodyDisc(tracks[b].Samples[i]))
				}
				if slack, _, _ := project(ds).box(ev.Frame.Rotation, sh.w, sh.h); slack < 0 {
					t.Errorf("%s: at %s the bodies miss the frame at its %.1f° rotation by %.3f°", ev.ID, at, ev.Frame.Rotation, -slack)
				}
			}
		}
	}
}

func TestMosaicsDoNotDependOnHowFarTheSearchLooks(t *testing.T) {
	t.Parallel()
	e, dsos, fx := rarityInputs(t)
	from, to := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	short := search(t, e, fx.Site, dsos, from, to)
	long := search(t, e, fx.Site, dsos, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
	within := func(a, b Event) bool {
		for _, m := range membersOf(a) {
			if !slices.Contains(membersOf(b), m) {
				return false
			}
		}
		return true
	}
	for _, s := range short {
		if s.Kind != EventMosaic2 {
			continue
		}
		found := false
		for _, l := range long {
			if l.Kind == EventMosaic2 && within(s, l) && l.Start.Before(s.End) && s.Start.Before(l.End) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is found in a four-day search but not in a year's", s.ID)
		}
	}
}

func TestAPlanFollowsItsGroupWhenABodyJoins(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	end := at.Add(12 * time.Hour)
	triple := Event{ID: "2-panel-moon-mars-jupiter", Kind: EventMosaic2, Start: at, End: end,
		Members: []Member{{Kind: KindMoon, ID: "body:moon"}, {Kind: KindPlanet, ID: "body:mars"}, {Kind: KindPlanet, ID: "body:jupiter"}}}
	planned := app.SkyEventChoice{EventID: "moon-jupiter", Bodies: "body:jupiter,body:moon", CommandID: "cmd", PlannedAt: &at, WindowStart: &at, WindowEnd: &end}
	unplanned := planned
	unplanned.CommandID, unplanned.PlannedAt = "", nil
	if c, ok := matchChoice([]app.SkyEventChoice{planned}, triple); !ok || c.EventID != "moon-jupiter" {
		t.Error("the planned pair does not follow its group")
	}
	if _, ok := matchChoice([]app.SkyEventChoice{unplanned}, triple); ok {
		t.Error("an unplanned choice follows another group")
	}
	if _, ok := matchOwnChoice([]app.SkyEventChoice{planned}, triple); ok {
		t.Error("the plan of the pair counts as the triple's own choice")
	}
}
