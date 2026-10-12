package bodies_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	night12   = "2026-10-12"
	cometGUID = "aaaa-comet"
	moonGUID  = "bbbb-near"
	idleGUID  = "cccc-idle"
)

func testSite() sky.Site {
	e := 200.0
	return sky.Site{Latitude: 32, Longitude: -97, Elevation: &e}
}

func open(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	return db
}

func exec(t *testing.T, db *gorm.DB, sql ...string) {
	t.Helper()
	for _, s := range sql {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func scheduler(t *testing.T, withTargets, withPositions bool) *gorm.DB {
	t.Helper()
	db := open(t)
	exec(t, db,
		`create table project ("Id" integer primary key, name text, state integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, projectid integer, guid text, ra double precision, "dec" double precision)`,
		`insert into project values (1, 'Comets', 1, 'p1'), (2, 'Closed', 3, 'p2')`,
		`insert into target values (1, 'C/2025 R2 (SWAN)', 1, 1, '`+cometGUID+`', 5, 27), (2, 'Close one', 1, 1, '`+moonGUID+`', 14, -22), (3, 'Old', 1, 2, '`+idleGUID+`', 1, 1), (4, 'M 31', 1, 1, 'dddd-fixed', 0.7, 41)`)
	if withTargets {
		exec(t, db, bodies.TargetSchema,
			`insert into ts_target_body (target_guid, designation) values ('`+cometGUID+`', 'C/2025 R2'), ('`+idleGUID+`', '2P')`)
	}
	if withPositions {
		exec(t, db, bodies.Schema)
	}
	return db
}

type fakeHorizons struct {
	srv   *httptest.Server
	hits  atomic.Int32
	fine  atomic.Int32
	cmds  chan string
	comet string
	moon  string
}

func newFake(t *testing.T) *fakeHorizons {
	t.Helper()
	read := func(n string) string {
		b, err := os.ReadFile("../horizons/testdata/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	f := &fakeHorizons{comet: read("comet-c2025r2-10m.json"), moon: read("moon-10m.json"), cmds: make(chan string, 100)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		q := r.URL.Query()
		f.cmds <- q.Get("COMMAND")
		if q.Get("STEP_SIZE") == "'1 m'" {
			f.fine.Add(1)
		}
		if strings.Contains(q.Get("COMMAND"), "NEAR") {
			_, _ = w.Write([]byte(f.moon))
			return
		}
		_, _ = w.Write([]byte(f.comet))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func service(t *testing.T, sched *gorm.DB, f *fakeHorizons, mode string) *bodies.Service {
	t.Helper()
	cache := open(t)
	if err := cache.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	return &bodies.Service{Sched: sched, Mode: mode, Now: func() time.Time { return now },
		Horizons: &horizons.Client{BaseURL: f.srv.URL, Cache: cache},
		Site:     func(context.Context) (sky.Site, error) { return testSite(), nil }}
}

func count(t *testing.T, db *gorm.DB, where string) int64 {
	t.Helper()
	var n int64
	q := db.Table(bodies.PositionTable)
	if where != "" {
		q = q.Where(where)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMissingTargetTableIsInert(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	s := service(t, scheduler(t, false, false), f, bodies.ModeOn)
	res, err := s.Refresh(context.Background())
	if err != nil || res.Skipped != bodies.SkipNoTargets {
		t.Fatalf("skipped %q, %v", res.Skipped, err)
	}
	if f.hits.Load() != 0 {
		t.Errorf("%d Horizons requests with no targets", f.hits.Load())
	}
	nilSched := service(t, nil, f, bodies.ModeOn)
	if res, err := nilSched.Refresh(context.Background()); err != nil || res.Skipped != bodies.SkipNoScheduler {
		t.Fatalf("no scheduler: %q %v", res.Skipped, err)
	}
	off := service(t, scheduler(t, true, true), f, bodies.ModeOff)
	if res, err := off.Refresh(context.Background()); err != nil || res.Skipped != bodies.SkipOff || f.hits.Load() != 0 {
		t.Fatalf("off: %q %v %d", res.Skipped, err, f.hits.Load())
	}
}

func TestMissingPositionTableComputesButWritesNothing(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, false)
	s := service(t, sched, f, bodies.ModeOn)
	res, err := s.Refresh(context.Background())
	if err != nil || res.Skipped != bodies.SkipNoTable {
		t.Fatalf("skipped %q, %v", res.Skipped, err)
	}
	if res.Targets != 1 || len(res.Tables) != bodies.Nights || res.Rows != 73 {
		t.Fatalf("targets %d tables %d rows %d", res.Targets, len(res.Tables), res.Rows)
	}
	if has := sched.Migrator().HasTable(bodies.PositionTable); has {
		t.Error("the stacker created ts_body_position")
	}
}

func TestPublishesOncePerTargetNightAndPrunes(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	s := service(t, sched, f, bodies.ModeOn)
	ctx := context.Background()
	res, err := s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.PublishedAt == nil || res.Changed == 0 {
		t.Fatalf("not published: %+v", res.Status)
	}
	if got := f.hits.Load(); got != bodies.Nights {
		t.Fatalf("%d Horizons requests, want one per night (%d)", got, bodies.Nights)
	}
	if cmd := <-f.cmds; cmd != "'DES=C/2025 R2;CAP;NOFRAG'" {
		t.Errorf("COMMAND %s", cmd)
	}
	if n := count(t, sched, ""); n != 73 {
		t.Fatalf("%d rows published, want 73 (both nights' fixture rows share times)", n)
	}
	if n := count(t, sched, "target_guid = '"+idleGUID+"'"); n != 0 {
		t.Errorf("a closed project's body was published")
	}
	var row bodies.PositionRow
	if err := sched.Table(bodies.PositionTable).Where("target_guid = ?", cometGUID).Order("at").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Designation != "C/2025 R2" || row.RA < 76.19 || row.RA > 76.2 || row.RARate > -13 || row.Mag == nil || row.Delta == nil {
		t.Errorf("row %+v", row)
	}

	again, err := s.Refresh(ctx)
	if err != nil || again.Skipped != bodies.SkipUnchanged {
		t.Fatalf("second refresh %q %v", again.Skipped, err)
	}
	if got := f.hits.Load(); got != bodies.Nights {
		t.Fatalf("%d Horizons requests after a second refresh, want %d", got, bodies.Nights)
	}

	exec(t, sched, `delete from ts_target_body`)
	gone, err := s.Refresh(ctx)
	if err != nil || gone.Deleted != 73 || count(t, sched, "") != 0 {
		t.Fatalf("after the designation was removed: %+v %v", gone.Status, err)
	}
}

func TestNearEarthBodyUsesMinuteSteps(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	exec(t, sched, `delete from ts_target_body`, `insert into ts_target_body (target_guid, designation) values ('`+moonGUID+`', 'DES=NEAR;')`)
	s := service(t, sched, f, bodies.ModeDryRun)
	res, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.fine.Load() != bodies.Nights || f.hits.Load() != 2*bodies.Nights {
		t.Fatalf("%d fine of %d requests", f.fine.Load(), f.hits.Load())
	}
	for _, tab := range res.Tables {
		if tab.StepSeconds != 60 || tab.MinDelta == nil || *tab.MinDelta > bodies.NearEarth {
			t.Errorf("table %+v", tab)
		}
	}
	if count(t, sched, "") != 0 || res.PublishedAt != nil || res.Changed == 0 {
		t.Errorf("dry run wrote rows or found nothing to change: %+v", res.Status)
	}
}

func TestHorizonsErrorKeepsPublishedRows(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	s := service(t, sched, f, bodies.ModeOn)
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	broken := service(t, sched, f, bodies.ModeOn)
	broken.Horizons = &horizons.Client{BaseURL: "http://127.0.0.1:1", Attempts: 1}
	res, err := broken.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != bodies.Nights || res.Deleted != 0 || count(t, sched, "") != 73 {
		t.Fatalf("errors %d deleted %d rows %d", len(res.Errors), res.Deleted, count(t, sched, ""))
	}
}

func TestSpansCoverEachNight(t *testing.T) {
	t.Parallel()
	site := testSite()
	for now, first := range map[time.Time]string{
		time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC):  night12,
		time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC):   night12,
		time.Date(2026, 10, 13, 12, 30, 0, 0, time.UTC): night12,
		time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC):  "2026-10-13",
	} {
		spans := bodies.Spans(site, now, 2)
		if len(spans) != 2 {
			t.Fatalf("%d spans", len(spans))
		}
		next, _ := time.Parse(time.DateOnly, first)
		if spans[0].Night != first || spans[1].Night != next.AddDate(0, 0, 1).Format(time.DateOnly) {
			t.Errorf("at %v nights %s %s, want %s first", now, spans[0].Night, spans[1].Night, first)
		}
		if !now.Before(spans[0].Stop) {
			t.Errorf("at %v the first span already ended %v", now, spans[0].Stop)
		}
		for _, sp := range spans {
			if sp.Start.Truncate(bodies.CoarseStep) != sp.Start || sp.Stop.Truncate(bodies.CoarseStep) != sp.Stop {
				t.Errorf("span off the step grid: %v %v", sp.Start, sp.Stop)
			}
			if site.SunAltitude(sp.Start) < 0 || site.SunAltitude(sp.Stop) < 0 {
				t.Errorf("span %v-%v starts or ends in the dark", sp.Start, sp.Stop)
			}
			if site.SunAltitude(sp.Start.Add(bodies.SpanPad+bodies.CoarseStep)) > 0 && site.SunAltitude(sp.Start.Add(bodies.SpanPad+2*bodies.CoarseStep)) > 0 {
				t.Errorf("span %v starts more than a step before sunset", sp.Start)
			}
			if h := sp.Stop.Sub(sp.Start).Hours(); h < 11 || h > 15 {
				t.Errorf("span of %.1f h", h)
			}
		}
	}
	late := bodies.Spans(site, time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC), 1)
	if late[0].Night != "2026-10-13" {
		t.Errorf("after local noon the night is %s", late[0].Night)
	}
}

func TestDesignationsByName(t *testing.T) {
	t.Parallel()
	m, err := bodies.DesignationsByName(context.Background(), scheduler(t, true, false))
	if err != nil {
		t.Fatal(err)
	}
	if m["C/2025 R2 (SWAN)"] != "C/2025 R2" || m["Old"] != "2P" || len(m) != 2 {
		t.Errorf("%v", m)
	}
	if m, err := bodies.DesignationsByName(context.Background(), scheduler(t, false, false)); err != nil || len(m) != 0 {
		t.Errorf("without the table: %v %v", m, err)
	}
}
