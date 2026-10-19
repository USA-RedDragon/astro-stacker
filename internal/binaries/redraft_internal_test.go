package binaries

import (
	"context"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
)

type undoCount struct {
	noCommands
	undone *[]string
}

func (u undoCount) Undo(_ context.Context, id, _ string) (schedcmd.Record, error) {
	*u.undone = append(*u.undone, id)
	return schedcmd.Record{}, nil
}

func requestFor(p Predicted, shift time.Duration) app.EclipseRequest {
	p.Mid = p.Mid.Add(shift)
	start, end := p.Window()
	return app.EclipseRequest{Kind: p.Kind, Mid: p.Mid, Start: start, End: end, State: app.EclipseSent}
}

func TestMovedOutThreshold(t *testing.T) {
	t.Parallel()
	mid := time.Date(2026, 11, 2, 4, 0, 0, 0, time.UTC)
	long := Predicted{Kind: KindPrimary, Mid: mid, Duration: 2 * time.Hour, Err: 10 * time.Minute}
	short := Predicted{Kind: KindPrimary, Mid: mid, Duration: 20 * time.Minute, Err: time.Minute}
	narrower, wider := long, long
	narrower.Err, wider.Err = 0, 30*time.Minute
	cases := []struct {
		name  string
		req   app.EclipseRequest
		now   Predicted
		moved bool
	}{
		{"same eclipse", requestFor(long, 0), long, false},
		{"a 7 h window moved 20 min, inside its 42 min slack", requestFor(long, 20*time.Minute), long, false},
		{"a 7 h window moved 40 min earlier", requestFor(long, 40*time.Minute), long, false},
		{"a 7 h window moved an hour", requestFor(long, -time.Hour), long, true},
		{"the timing error shrank, the old window holds the new one", requestFor(long, 0), narrower, false},
		{"the timing error grew an hour each side", requestFor(long, 0), wider, true},
		{"a 66 min window moved 8 min, inside the 10 min floor", requestFor(short, 8*time.Minute), short, false},
		{"a 66 min window moved 12 min", requestFor(short, -12*time.Minute), short, true},
	}
	for _, c := range cases {
		if got := movedOut(c.req, c.now); got != c.moved {
			t.Errorf("%s: moved %v, want %v", c.name, got, c.moved)
		}
	}
}

func TestRedraftMovedRequests(t *testing.T) {
	t.Parallel()
	site := sky.Site{Latitude: 40, Longitude: 0}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	db := seasonalDB(t, site, now)
	var undone []string
	svc := &Service{DB: db, Commands: undoCount{undone: &undone}, Now: func() time.Time { return now },
		Site:     func(context.Context) (sky.Site, error) { return site, nil },
		Snapshot: func(context.Context) (*planning.Snapshot, error) { return &planning.Snapshot{}, nil }}
	ctx := context.Background()
	stars, err := svc.Binaries(ctx)
	if err != nil || len(stars) != 1 {
		t.Fatalf("stars %d %v", len(stars), err)
	}
	st := stars[0]
	e, _ := st.ephemeris()
	ra, dec := st.Facts.Gaia.Now(now)
	var prim []Predicted
	for _, p := range Predict(e, st.deeper(), ra, dec, now.Add(24*time.Hour), now.Add(10*24*time.Hour)) {
		if p.Kind == KindPrimary {
			prim = append(prim, st.withPhaseError(p, e))
		}
	}
	if len(prim) < 3 {
		t.Fatalf("predictions %d", len(prim))
	}
	kept := requestFor(prim[0], 2*time.Minute)
	movedAuto := requestFor(prim[1], 90*time.Minute)
	movedAuto.Auto = true
	movedOwn := requestFor(prim[2], -3*time.Hour)
	begun := requestFor(Predicted{Kind: KindPrimary, Mid: now.Add(time.Hour), Duration: time.Hour}, 0)
	reqs := []*app.EclipseRequest{&kept, &movedAuto, &movedOwn, &begun}
	for i, r := range reqs {
		r.SourceID, r.CreateCmd = st.Source.ID, []string{"keep", "auto", "own", "begun"}[i]
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.redraftMoved(ctx, stars); err != nil {
		t.Fatal(err)
	}
	state := func(id int) app.EclipseRequest {
		var r app.EclipseRequest
		if err := db.First(&r, id).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := state(kept.ID); r.State != app.EclipseSent {
		t.Errorf("a request 2 min off is left alone: %s", r.State)
	}
	if r := state(begun.ID); r.State != app.EclipseSent {
		t.Errorf("a request whose window began is left alone: %s", r.State)
	}
	for _, id := range []int{movedAuto.ID, movedOwn.ID} {
		if r := state(id); r.State != app.EclipseMoved || r.Note == "" {
			t.Errorf("request %d moved over an hour is withdrawn: %+v", id, r)
		}
	}
	if len(undone) != 2 || undone[0] != "auto" || undone[1] != "own" {
		t.Errorf("withdrawn commands %v", undone)
	}
	var again []app.EclipseRequest
	if err := db.Where("id > ?", begun.ID).Find(&again).Error; err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Auto || absDur(again[0].Mid.Sub(prim[2].Mid)) > time.Minute {
		t.Fatalf("the user's request is asked for again at the new time %s: %+v", prim[2].Mid, again)
	}
	stars, err = svc.Binaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var all []app.EclipseRequest
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if clash(all, st.Source.ID, prim[1], prim[1].Mid.Add(-time.Minute), prim[1].Mid.Add(time.Minute), now) {
		t.Error("a moved request does not block asking for its eclipse at the new time")
	}
	if sv := seasonView(stars[0], all, now); sv == nil || sv.Stopped != "" {
		t.Errorf("a moved request does not stop the season: %+v", sv)
	}
}

// TestRelabelledEclipsesKeepTheirRequests is a star the vet first took as
// one dip every 2.741969 days and later as two equal eclipses every
// 5.483938 days, as source 16040: every other eclipse turns from primary
// to secondary at the same time, and no request may be drafted again.
func TestRelabelledEclipsesKeepTheirRequests(t *testing.T) {
	t.Parallel()
	const p, t0 = 2.741969, 2460000.3
	one := &period.Ephemeris{Period: p, PeriodErr: 3e-6, T0: t0, T0Err: 0.0004, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.02, Shape: 1, Depth: map[string]float64{keyZTFr: 0.47}}}}
	two := &period.Ephemeris{Period: 2 * p, PeriodErr: 6e-6, T0: t0, T0Err: 0.0004, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{
			{Phase: 0, Width: 0.01, Shape: 1, Depth: map[string]float64{keyZTFr: 0.47}},
			{Phase: 0.5, Width: 0.01, Shape: 1, Depth: map[string]float64{keyZTFr: 0.46}},
		}}
	gaia := &GaiaFacts{ID: 1, RA: 280, Dec: 30, G: 16}
	before := Star{Facts: &Facts{Ephemeris: one, Gaia: gaia}}
	after := Star{Facts: &Facts{Ephemeris: two, Gaia: gaia}}
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	preds := Predict(one, 0, gaia.RA, gaia.Dec, from, from.Add(30*24*time.Hour))
	if len(preds) < 8 {
		t.Fatalf("predictions %d", len(preds))
	}
	kinds := map[string]int{}
	for _, pr := range preds {
		r := requestFor(pr, 0)
		if q, ok := nearestPrediction(before, r); !ok || movedOut(r, q) {
			t.Fatalf("request at %s moved under its own ephemeris", r.Mid)
		}
		q, ok := nearestPrediction(after, r)
		if !ok || movedOut(r, q) {
			t.Errorf("request at %s is drafted again though only its kind changed (nearest %+v)", r.Mid, q)
		}
		kinds[q.Kind]++
	}
	if kinds[KindPrimary] == 0 || kinds[KindSecondary] == 0 {
		t.Errorf("kinds after doubling %v, want both", kinds)
	}
	var rows []app.EclipseTiming
	for _, pr := range preds {
		rows = append(rows, app.EclipseTiming{SourceID: 1, Kind: KindPrimary, Predicted: pr.Mid})
	}
	for _, pr := range Predict(two, 0, gaia.RA, gaia.Dec, from, from.Add(30*24*time.Hour)) {
		if r := matchTiming(rows, 1, pr.Kind, pr.Mid, two.Period); r == nil || absDur(r.Predicted.Sub(pr.Mid)) > time.Minute {
			t.Errorf("the %s eclipse at %s does not find its stored timing", pr.Kind, pr.Mid)
		}
	}
}
