package bodies_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const hartley = "161P"

func TestMotionPA(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		ra, dec, want float64
	}{{0, 10, 0}, {10, 0, 90}, {0, -10, 180}, {-10, 0, 270}, {-293.829, 113.4591, 291.1}} {
		got, ok := bodies.MotionPA(c.ra, c.dec)
		if !ok || math.Abs(got-c.want) > 0.05 {
			t.Errorf("MotionPA(%v, %v) = %v %v, want %v", c.ra, c.dec, got, ok, c.want)
		}
	}
	if _, ok := bodies.MotionPA(0, 0); ok {
		t.Error("a body at rest has a motion angle")
	}
}

func TestTrackRowsPicksTheHighestDarkRow(t *testing.T) {
	t.Parallel()
	site := testSite()
	pa := 84.0
	at := time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)
	tab := horizons.Table{Rows: []horizons.Row{
		{At: at.Add(-5 * time.Hour), RA: 321, Dec: 5, RARate: 60, DecRate: 0, PsAng: &pa},
		{At: at, RA: 321, Dec: 5, RARate: 60, DecRate: 0, PsAng: &pa},
		{At: at.Add(3 * time.Hour), RA: 321, Dec: 5, RARate: 60, DecRate: 0, PsAng: &pa},
	}}
	rows, best := bodies.TrackRows(site, tab)
	if len(rows) != 3 || best == nil {
		t.Fatalf("rows %d best %v", len(rows), best)
	}
	if rows[0].Dark {
		t.Error("an afternoon row is dark")
	}
	if !best.Dark || !best.At.Equal(at) {
		t.Errorf("best %v dark %v", best.At, best.Dark)
	}
	if best.TailPA == nil || *best.TailPA != 84 || best.MotionPA == nil || *best.MotionPA != 90 || best.RateMin != 1 {
		t.Errorf("best %+v", best)
	}
}

func TestTrackUsesThePositionTablesCacheKey(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/horizons-topo-161p-psang.json")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	db := open(t)
	if err := db.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	hz := &horizons.Client{BaseURL: srv.URL, Cache: db, Now: func() time.Time { return now }}
	site := testSite()
	siteFn := func(context.Context) (sky.Site, error) { return site, nil }
	svc := &bodies.Service{Horizons: hz, Site: siteFn, Now: func() time.Time { return now }}
	ns := bodies.Spans(site, now, bodies.Nights)[0]
	if _, _, err := svc.Table(context.Background(), bodies.Target{Designation: hartley}, site, ns); err != nil {
		t.Fatal(err)
	}
	tr := &bodies.Tracker{Horizons: hz, Site: siteFn, Now: func() time.Time { return now }}
	view, err := tr.Track(context.Background(), " "+hartley+" ", "")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d Horizons requests, want 1: the track did not reuse the position table", hits.Load())
	}
	if !view.Cached || !view.Angles || view.Kind != bodies.KindComet || view.Designation != hartley || view.Command != "DES=161P;CAP;NOFRAG" {
		t.Fatalf("view %+v", view)
	}
	if len(view.Rows) != 13 || view.Best == nil || view.Best.TailPA == nil || math.Abs(*view.Best.TailPA-84.06) > 0.01 {
		t.Fatalf("rows %d best %+v", len(view.Rows), view.Best)
	}
	if view.Best.VelocityPA == nil || math.Abs(*view.Best.VelocityPA-141.67) > 0.05 || view.Best.MotionPA == nil {
		t.Fatalf("best angles %+v", view.Best)
	}
	if !view.Night.Start.Equal(ns.Start) || !view.Night.Stop.Equal(ns.Stop) {
		t.Fatalf("night %+v, want %+v", view.Night, ns)
	}
}

func TestTrackAsksForAsteroidsAsSmallBodies(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var cmds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cmds = append(cmds, r.URL.Query().Get("COMMAND"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"result":"No matches found."}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	tr := &bodies.Tracker{Horizons: &horizons.Client{BaseURL: srv.URL, Now: func() time.Time { return now }},
		Site: func(context.Context) (sky.Site, error) { return testSite(), nil }, Now: func() time.Time { return now }}
	for des, want := range map[string]string{"1": "1;", tc6: tc6Cmd} {
		view, _ := tr.Track(context.Background(), des, bodies.KindAsteroid)
		if view.Command != want {
			t.Errorf("%s: command %q, want %q", des, view.Command, want)
		}
	}
	view, _ := tr.Track(context.Background(), "1", "")
	if view.Command != "DES=1;CAP;NOFRAG" {
		t.Errorf("a target with no kind is a comet: %q", view.Command)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(cmds, ",") != "'1;','DES=2026 TC6;','DES=1;CAP;NOFRAG'" && strings.Join(cmds, ",") != "'DES=2026 TC6;','1;','DES=1;CAP;NOFRAG'" {
		t.Errorf("commands %v", cmds)
	}
}

func TestTrackNeedsADesignationAndASite(t *testing.T) {
	t.Parallel()
	tr := &bodies.Tracker{}
	if _, err := tr.Track(context.Background(), " ", "comet"); !errors.Is(err, bodies.ErrNoDesignation) {
		t.Errorf("err %v", err)
	}
	if _, err := tr.Track(context.Background(), hartley, "comet"); !errors.Is(err, bodies.ErrNoSite) {
		t.Errorf("err %v", err)
	}
}

func TestTargetsReadFramingWhenTheColumnsExist(t *testing.T) {
	t.Parallel()
	db := scheduler(t, true, false)
	exec(t, db, `update ts_target_body set rotation_deg = 84.06, offset_frac = -0.25 where target_guid = '`+cometGUID+`'`)
	got, ok, err := bodies.TargetByGUID(context.Background(), db, cometGUID)
	if err != nil || !ok {
		t.Fatalf("%v %v", got, err)
	}
	if got.RotationDeg == nil || *got.RotationDeg != 84.06 || got.OffsetFrac == nil || *got.OffsetFrac != -0.25 {
		t.Fatalf("framing %v %v", got.RotationDeg, got.OffsetFrac)
	}
	idle, ok, err := bodies.TargetByGUID(context.Background(), db, idleGUID)
	if err != nil || !ok || idle.RotationDeg != nil || idle.OffsetFrac != nil {
		t.Fatalf("a target without framing: %+v %v", idle, err)
	}
	if none, ok, err := bodies.TargetByGUID(context.Background(), db, "dddd-fixed"); err != nil || ok {
		t.Fatalf("a fixed target: %+v %v", none, err)
	}
}

func TestTargetsWithoutFramingColumns(t *testing.T) {
	t.Parallel()
	db := scheduler(t, false, false)
	exec(t, db, `create table ts_target_body (target_guid text NOT NULL PRIMARY KEY, designation text NOT NULL, kind text NOT NULL DEFAULT 'comet', updated_at text)`,
		`insert into ts_target_body (target_guid, designation) values ('`+cometGUID+`', '`+swan+`')`)
	if ok, err := bodies.HasFraming(context.Background(), db); err != nil || ok {
		t.Fatalf("HasFraming %v %v", ok, err)
	}
	got, ok, err := bodies.TargetByGUID(context.Background(), db, cometGUID)
	if err != nil || !ok || got.RotationDeg != nil || got.OffsetFrac != nil {
		t.Fatalf("%+v %v", got, err)
	}
}
