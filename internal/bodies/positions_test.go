package bodies_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	swan      = "C/2025 R2"
	r1b       = "C/2023 R1-B"
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
			`insert into ts_target_body (target_guid, designation) values ('`+cometGUID+`', '`+swan+`'), ('`+idleGUID+`', '2P')`)
	}
	if withPositions {
		exec(t, db, bodies.Schema)
	}
	return db
}

type fakeHorizons struct {
	srv  *httptest.Server
	hits atomic.Int32
	fine atomic.Int32
	cmds chan string
}

const fakeHeader = " Date__(UT)__HR:MN, , , R.A.___(ICRF), DEC____(ICRF),  R.A.__(a-app), DEC___(a-app),  dRA*cosD, d(DEC)/dt,    T-mag,  N-mag,             delta,     deldot,  RA_3sigma, DEC_3sigma,"

// newFake answers like Horizons over the requested span and step: a slow
// comet, a body nearer than NearEarth for commands with NEAR, and no match
// for commands with 2099.
func newFake(t *testing.T) *fakeHorizons {
	t.Helper()
	f := &fakeHorizons{cmds: make(chan string, 100)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		q := r.URL.Query()
		cmd := q.Get("COMMAND")
		f.cmds <- cmd
		if strings.Contains(cmd, "2099") {
			_, _ = fmt.Fprintf(w, `{"result":%q}`, "Matching small-bodies: \n    No matches found.\n")
			return
		}
		parse := func(k string) time.Time {
			v, err := time.Parse("2006-01-02 15:04", strings.Trim(q.Get(k), "'"))
			if err != nil {
				t.Errorf("%s: %v", k, err)
			}
			return v
		}
		step := 10 * time.Minute
		if q.Get("STEP_SIZE") == "'1 m'" {
			f.fine.Add(1)
			step = time.Minute
		}
		delta := 4.836
		if strings.Contains(cmd, "NEAR") {
			delta = 0.0026
		}
		var b strings.Builder
		b.WriteString("****\n" + fakeHeader + "\n****\n$$SOE\n")
		for at := parse("START_TIME"); !at.After(parse("STOP_TIME")); at = at.Add(step) {
			m := at.Sub(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)).Minutes()
			fmt.Fprintf(&b, " %s, , , %.9f, %.9f, %.9f, %.9f, -13.37, 0.83, 24.3, n.a., %.8f, -7.7, 2.86, 0.27,\n",
				at.Format("2006-Jan-02 15:04"), 76.1955-m*7e-5, 27.8636+m*4e-6, 76.62-m*7e-5, 27.9009+m*4e-6, delta)
		}
		b.WriteString("$$EOE\n")
		_, _ = fmt.Fprintf(w, `{"result":%q}`, b.String())
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// rowsPerNight is how many 10-minute rows the fake gives each night.
func rowsPerNight(now time.Time) int64 {
	var n int64
	for _, sp := range bodies.Spans(testSite(), now, bodies.Nights) {
		n += int64(sp.Stop.Sub(sp.Start)/bodies.CoarseStep) + 1
	}
	return n
}

func fakeNow() time.Time { return time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC) }

func service(t *testing.T, sched *gorm.DB, f *fakeHorizons, mode string) *bodies.Service {
	t.Helper()
	cache := open(t)
	if err := cache.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	return &bodies.Service{Sched: sched, Mode: mode, Now: fakeNow,
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
	if f.hits.Load() != 0 || res.Rows != 0 {
		t.Fatalf("%d Horizons requests, %d rows without ts_body_position", f.hits.Load(), res.Rows)
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
	want := rowsPerNight(fakeNow())
	if n := count(t, sched, ""); n != want || int64(res.Rows) != want {
		t.Fatalf("%d rows published of %d computed, want %d over two nights", n, res.Rows, want)
	}
	if n := count(t, sched, "night = '"+night12+"'"); n == 0 || n == want {
		t.Errorf("%d of %d rows are the first night's", n, want)
	}
	if n := count(t, sched, "target_guid = '"+idleGUID+"'"); n != 0 {
		t.Errorf("a closed project's body was published")
	}
	var row bodies.PositionRow
	if err := sched.Table(bodies.PositionTable).Where("target_guid = ?", cometGUID).Order("at").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Designation != swan || row.RA < 76.19 || row.RA > 76.21 || row.RARate > -13 || row.Mag == nil || row.Delta == nil {
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
	if err != nil || int64(gone.Deleted) != want || count(t, sched, "") != 0 {
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
	if len(res.Errors) != bodies.Nights || res.Deleted != 0 || count(t, sched, "") != rowsPerNight(fakeNow()) {
		t.Fatalf("errors %d deleted %d rows %d", len(res.Errors), res.Deleted, count(t, sched, ""))
	}
}

func TestDesignationChangeDropsOldRowsEvenWhenTheFetchFails(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	s := service(t, sched, f, bodies.ModeOn)
	ctx := context.Background()
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	exec(t, sched, `update ts_target_body set designation = 'C/2099 Z9' where target_guid = '`+cometGUID+`'`)
	res, err := s.Refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != bodies.Nights || int64(res.Deleted) != rowsPerNight(fakeNow()) || count(t, sched, "") != 0 {
		t.Fatalf("errors %d deleted %d left %d", len(res.Errors), res.Deleted, count(t, sched, ""))
	}
}

func TestDryRunReportsRealChanges(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	s := service(t, sched, f, bodies.ModeDryRun)
	for range 2 {
		res, err := s.Refresh(context.Background())
		if err != nil || int64(res.Changed) != rowsPerNight(fakeNow()) || res.PublishedAt != nil || count(t, sched, "") != 0 {
			t.Fatalf("dry run: %+v %v", res.Status, err)
		}
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

func TestTargetsByName(t *testing.T) {
	t.Parallel()
	sched := scheduler(t, true, false)
	exec(t, sched, `insert into ts_target_body (target_guid, designation, kind) values ('dddd-fixed', '4', 'asteroid')`)
	m, err := bodies.TargetsByName(context.Background(), sched)
	if err != nil {
		t.Fatal(err)
	}
	if b := m["C/2025 R2 (SWAN)"]; b.Designation != swan || b.Kind != bodies.KindComet {
		t.Errorf("comet %+v", b)
	}
	if b := m["M 31"]; b.Designation != "4" || b.Kind != bodies.KindAsteroid {
		t.Errorf("asteroid %+v", b)
	}
	if m["Old"].Designation != "2P" || len(m) != 3 {
		t.Errorf("%v", m)
	}
	if m, err := bodies.TargetsByName(context.Background(), scheduler(t, false, false)); err != nil || len(m) != 0 {
		t.Errorf("without the table: %v %v", m, err)
	}
}

func TestAsteroidTargetsUseTheSmallBodyCommand(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	sched := scheduler(t, true, true)
	exec(t, sched, `delete from ts_target_body`,
		`insert into ts_target_body (target_guid, designation, kind) values ('`+cometGUID+`', '1', 'asteroid'), ('`+moonGUID+`', '2026 TC6', 'asteroid')`)
	s := service(t, sched, f, bodies.ModeDryRun)
	res, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	close(f.cmds)
	got := map[string]int{}
	for c := range f.cmds {
		got[c]++
	}
	if got["'1;'"] != bodies.Nights || got["'"+tc6Cmd+"'"] != bodies.Nights || len(got) != 2 {
		t.Errorf("commands %v", got)
	}
	for _, tab := range res.Tables {
		if want := map[string]string{"1": "1;", tc6: tc6Cmd}[tab.Designation]; tab.Command != want {
			t.Errorf("%s: command %q, want %q", tab.Designation, tab.Command, want)
		}
	}
	if len(res.Tables) != 2*bodies.Nights {
		t.Errorf("%d tables", len(res.Tables))
	}
}
