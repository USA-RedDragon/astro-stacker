package bodies_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"gorm.io/gorm"
)

const (
	hartleyGUID  = "1111-hartley"
	vestaGUID    = "2222-vesta"
	k2GUID       = "3333-k2"
	k2           = "C/2017 K2"
	vesta        = "Vesta"
	vestaCommand = "'4;'"
)

func seasonSite() sky.Site {
	e := 1000.0
	return sky.Site{Latitude: 30, Longitude: -110, Elevation: &e}
}

func seasonNow() time.Time { return time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC) }

func seasonFirst() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func seasonElements(t *testing.T) bodies.ElementsFunc {
	t.Helper()
	els, bad := bodies.ParseComets(fixture(t, "CometEls-season.txt"))
	if bad != 0 || len(els) != 2 {
		t.Fatalf("%d elements, %d unreadable", len(els), bad)
	}
	return func(_ context.Context, d string) (bodies.Elements, bool) {
		for _, el := range els {
			if bodies.SameDesignation(el.Designation, d) {
				return el, true
			}
		}
		return bodies.Elements{}, false
	}
}

type seasonCase struct {
	file   string
	target bodies.Target
}

func seasonCases() map[string]seasonCase {
	return map[string]seasonCase{
		hartley: {"horizons-season-161p.txt", bodies.Target{GUID: hartleyGUID, Designation: hartley, Kind: bodies.KindComet}},
		vesta:   {"horizons-season-vesta.txt", bodies.Target{GUID: vestaGUID, Designation: "4", Kind: bodies.KindAsteroid}},
		k2:      {"horizons-season-k2.txt", bodies.Target{GUID: k2GUID, Designation: k2, Kind: bodies.KindComet}},
	}
}

func seasonRows(t *testing.T, name string, withElements bool) []bodies.NightRow {
	t.Helper()
	c := seasonCases()[name]
	tab, err := horizons.Parse(fixture(t, c.file))
	if err != nil {
		t.Fatal(err)
	}
	var el *bodies.Elements
	if withElements {
		if e, ok := seasonElements(t)(context.Background(), c.target.Designation); ok {
			el = &e
		}
	}
	return bodies.SeasonRows(c.target, seasonSite(), tab, el, seasonNow())
}

func rowOf(t *testing.T, rows []bodies.NightRow, night string) bodies.NightRow {
	t.Helper()
	for _, r := range rows {
		if r.Night == night {
			return r
		}
	}
	t.Fatalf("no row for %s", night)
	return bodies.NightRow{}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// pluginSeason counts nights the way the plugin's SeasonCounter does: none
// when tonight is unusable, else the usable nights until 14 unusable in a
// row.
func pluginSeason(nights []bodies.NightDetail) (int, string) {
	if len(nights) == 0 || !nights[0].Usable {
		return 0, ""
	}
	n, gap, last := 0, 0, ""
	for _, d := range nights {
		if d.Usable {
			n, gap, last = n+1, 0, d.Night
			continue
		}
		if gap++; gap >= 14 {
			break
		}
	}
	return n, last
}

func TestNightTimesAreLocalMeanMidnight(t *testing.T) {
	t.Parallel()
	site := seasonSite()
	at := bodies.NightAt(site, seasonFirst())
	if want := time.Date(2026, 10, 13, 7, 20, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("NightAt %s, want %s", at, want)
	}
	if n := bodies.NightOf(site, at); n != night12 {
		t.Errorf("NightOf %s", n)
	}
	if n := bodies.NightOf(site, at.Add(11*time.Hour)); n != night12 {
		t.Errorf("NightOf late morning %s", n)
	}
	if got := bodies.Tonight(site, seasonNow()); !got.Equal(seasonFirst()) {
		t.Errorf("Tonight %s", got)
	}
	req := bodies.SeasonRequest(seasonCases()[vesta].target, site, seasonFirst())
	q := req.Values()
	if q.Get("STEP_SIZE") != "'1440 m'" || q.Get("START_TIME") != "'2026-10-12 07:20'" || q.Get("STOP_TIME") != "'2027-10-14 07:20'" || q.Get("COMMAND") != vestaCommand {
		t.Errorf("request %v", q)
	}
}

func TestSeasonRowsFromRecordedHorizons(t *testing.T) {
	t.Parallel()
	for name := range seasonCases() {
		rows := seasonRows(t, name, true)
		if len(rows) != bodies.SeasonNights+2 || rows[0].Night != "2026-10-11" || rows[len(rows)-1].Night != "2027-10-13" {
			t.Fatalf("%s: %d rows from %s to %s", name, len(rows), rows[0].Night, rows[len(rows)-1].Night)
		}
	}

	jpl := rowOf(t, seasonRows(t, hartley, false), "2026-10-11")
	if jpl.Mag == nil || *jpl.Mag != 14.68 || jpl.MagLimit == nil || *jpl.MagLimit != 15 || !jpl.Bright() {
		t.Errorf("161P from Horizons T-mag only: %+v", jpl)
	}
	both := rowOf(t, seasonRows(t, hartley, true), "2026-10-11")
	if both.Mag == nil || !near(*both.Mag, 9.56, 0.02) {
		t.Errorf("161P takes the brighter MPC estimate: mag %v", both.Mag)
	}
	if both.RA != jpl.RA || both.Dec != jpl.Dec || !near(both.RA, 322.964143, 1e-6) || both.RARate != -307.23 {
		t.Errorf("161P position %+v", both)
	}

	vesta := rowOf(t, seasonRows(t, vesta, true), "2026-10-11")
	if vesta.Mag == nil || *vesta.Mag != 6.39 || vesta.MagLimit == nil || *vesta.MagLimit != 12 {
		t.Errorf("Vesta uses APmag against V 12: %+v", vesta)
	}

	for _, r := range seasonRows(t, k2, true) {
		if r.Bright() || r.Mag == nil || *r.Mag < 17.5 {
			t.Fatalf("C/2017 K2 is bright enough on %s: %v", r.Night, r.Mag)
		}
	}
}

func TestNoMagnitudeMeansNoLimit(t *testing.T) {
	t.Parallel()
	tab := horizons.Table{Rows: []horizons.Row{{At: time.Date(2026, 10, 13, 7, 20, 0, 0, time.UTC), RA: 10, Dec: 20}}}
	rows := bodies.SeasonRows(bodies.Target{GUID: "g", Designation: "C/2099 A1", Kind: bodies.KindComet}, seasonSite(), tab, nil, seasonNow())
	if len(rows) != 1 || rows[0].Mag != nil || rows[0].MagLimit != nil || !rows[0].Bright() {
		t.Fatalf("%+v", rows)
	}
}

func TestSeasonOfRecordedBodies(t *testing.T) {
	t.Parallel()
	site := seasonSite()
	cases := []struct {
		name                   string
		bright, usable         int
		firstUsable, lastUsabl string
		season                 int
		seasonEnd              string
	}{
		{hartley, 189, 108, night12, "2027-04-18", 42, "2026-11-22"},
		{vesta, 366, 144, night12, "2027-10-12", 126, "2027-02-14"},
		{k2, 0, 0, "", "", 0, ""},
	}
	for _, c := range cases {
		nights := bodies.BuildNightView(site, seasonRows(t, c.name, true), seasonFirst(), 30, 60)
		if len(nights) != bodies.SeasonNights {
			t.Fatalf("%s: %d nights", c.name, len(nights))
		}
		bright, usable, first, last := 0, 0, "", ""
		for _, d := range nights {
			if d.Bright {
				bright++
			}
			if d.Usable {
				usable++
				if first == "" {
					first = d.Night
				}
				last = d.Night
			}
			if d.Usable && (d.DarkHours < 1 || d.PeakAlt == nil || *d.PeakAlt < 30 || !d.Bright) {
				t.Errorf("%s %s usable with %+v", c.name, d.Night, d)
			}
		}
		season, end := pluginSeason(nights)
		if bright != c.bright || usable != c.usable || first != c.firstUsable || last != c.lastUsabl || season != c.season || end != c.seasonEnd {
			t.Errorf("%s: bright %d usable %d (%s to %s) season %d to %s, want %d %d (%s to %s) %d to %s",
				c.name, bright, usable, first, last, season, end, c.bright, c.usable, c.firstUsable, c.lastUsabl, c.season, c.seasonEnd)
		}
	}
	tonight := bodies.BuildNightView(site, seasonRows(t, hartley, true), seasonFirst(), 30, 60)[0]
	if tonight.Night != night12 || tonight.PeakAlt == nil || !near(*tonight.PeakAlt, 64.8, 0.05) || tonight.DarkHours != 4.83 {
		t.Errorf("161P tonight %+v", tonight)
	}
	high := bodies.BuildNightView(site, seasonRows(t, hartley, true), seasonFirst(), 70, 60)[0]
	if high.Usable || high.DarkHours != 0 {
		t.Errorf("161P above 70 degrees for an hour %+v", high)
	}
}

type seasonFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	fail map[string]bool
	hits map[string]int
}

func newSeasonFake(t *testing.T) *seasonFake {
	t.Helper()
	f := &seasonFake{fail: map[string]bool{}, hits: map[string]int{}}
	files := map[string]string{}
	for _, c := range seasonCases() {
		files["'"+horizons.CommandFor(c.target.Designation, c.target.Kind)+"'"] = fixture(t, c.file)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("COMMAND")
		f.mu.Lock()
		f.hits[cmd]++
		fail := f.fail[cmd]
		f.mu.Unlock()
		body, ok := files[cmd]
		if fail || !ok || r.URL.Query().Get("STEP_SIZE") != "'1440 m'" {
			_, _ = fmt.Fprintf(w, `{"result":%q}`, "    No matches found.\n")
			return
		}
		_, _ = fmt.Fprintf(w, `{"result":%q}`, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *seasonFake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.hits {
		n += v
	}
	return n
}

func (f *seasonFake) setFail(cmd string, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail["'"+cmd+"'"] = fail
}

func seasonScheduler(t *testing.T, withNights bool) *gorm.DB {
	t.Helper()
	db := open(t)
	exec(t, db,
		`create table project ("Id" integer primary key, name text, state integer, guid text, minimumaltitude double precision, minimumtime integer)`,
		`create table target ("Id" integer primary key, name text, active integer, projectid integer, guid text, ra double precision, "dec" double precision)`,
		`insert into project values (1, 'Bodies', 1, 'p1', 30, 60)`,
		`insert into target values (1, '161P/Hartley-IRAS', 1, 1, '`+hartleyGUID+`', 0, 0), (2, '4 Vesta', 1, 1, '`+vestaGUID+`', 0, 0), (3, 'C/2017 K2 (PANSTARRS)', 1, 1, '`+k2GUID+`', 0, 0)`,
		bodies.TargetSchema,
		`insert into ts_target_body (target_guid, designation, kind) values ('`+hartleyGUID+`', '161P', 'comet'), ('`+vestaGUID+`', '4', 'asteroid'), ('`+k2GUID+`', '`+k2+`', 'comet')`)
	if withNights {
		exec(t, db, bodies.NightSchema)
	}
	return db
}

func nightService(t *testing.T, sched *gorm.DB, f *seasonFake, mode string) *bodies.NightService {
	t.Helper()
	return &bodies.NightService{Sched: sched, Mode: mode, Now: seasonNow, Elements: seasonElements(t),
		Horizons: &horizons.Client{BaseURL: f.srv.URL, Attempts: 1, FailureTTL: time.Nanosecond},
		Site:     func(context.Context) (sky.Site, error) { return seasonSite(), nil }}
}

func nightCount(t *testing.T, db *gorm.DB, where string) int64 {
	t.Helper()
	var n int64
	q := db.Table(bodies.NightTable)
	if where != "" {
		q = q.Where(where)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNightServicePublishesKeepsAndPrunes(t *testing.T) {
	t.Parallel()
	f := newSeasonFake(t)
	sched := seasonScheduler(t, true)
	s := nightService(t, sched, f, bodies.ModeOn)
	ctx := context.Background()
	per := int64(bodies.SeasonNights + 2)

	checkFirstPublish(t, s, sched, per)

	res, err := s.Refresh(ctx)
	if err != nil || res.Skipped != bodies.SkipUnchanged || res.Changed != 0 || res.Deleted != 0 {
		t.Fatalf("second publish %+v, %v", res, err)
	}

	checkFailedFetchKeepsRows(t, s, sched, f, per)
	checkInactiveTargetIsPruned(t, s, sched, per)
	checkView(t, s)
}

func checkFirstPublish(t *testing.T, s *bodies.NightService, sched *gorm.DB, per int64) {
	t.Helper()
	res, err := s.Refresh(context.Background())
	if err != nil || res.PublishedAt == nil || res.Targets != 3 || int64(res.Changed) != 3*per || res.From != night12 || res.To != "2027-10-13" {
		t.Fatalf("first publish %+v, %v", res, err)
	}
	if n := nightCount(t, sched, ""); n != 3*per {
		t.Fatalf("%d rows", n)
	}
	var row bodies.NightRow
	if err := sched.Table(bodies.NightTable).Where("target_guid = ? AND night = ?", hartleyGUID, night12).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Designation != hartley || row.MagLimit == nil || *row.MagLimit != 15 || row.Mag == nil || *row.Mag > 10 || !row.At.Equal(time.Date(2026, 10, 13, 7, 20, 0, 0, time.UTC)) {
		t.Errorf("161P row %+v", row)
	}
}

func checkFailedFetchKeepsRows(t *testing.T, s *bodies.NightService, sched *gorm.DB, f *seasonFake, per int64) {
	t.Helper()
	f.setFail("DES=161P;CAP;NOFRAG", true)
	defer f.setFail("DES=161P;CAP;NOFRAG", false)
	res, err := s.Refresh(context.Background())
	if err != nil || len(res.Errors) != 1 || res.Errors[0].TargetGUID != hartleyGUID || res.Deleted != 1 {
		t.Fatalf("failed fetch %+v, %v", res, err)
	}
	if n := nightCount(t, sched, "target_guid = '"+hartleyGUID+"'"); n != per-1 {
		t.Errorf("a failed fetch kept %d of 161P's rows, want all but the night before tonight", n)
	}
}

func checkInactiveTargetIsPruned(t *testing.T, s *bodies.NightService, sched *gorm.DB, per int64) {
	t.Helper()
	exec(t, sched, `update target set active = 0 where guid = '`+k2GUID+`'`)
	res, err := s.Refresh(context.Background())
	if err != nil || int64(res.Deleted) != per || res.Targets != 2 {
		t.Fatalf("inactive target %+v, %v", res, err)
	}
	if n := nightCount(t, sched, "target_guid = '"+k2GUID+"'"); n != 0 {
		t.Errorf("%d rows of an inactive target", n)
	}
	if n := nightCount(t, sched, ""); n != 2*per {
		t.Errorf("%d rows after pruning", n)
	}
}

func checkView(t *testing.T, s *bodies.NightService) {
	t.Helper()
	ctx := context.Background()
	v, ok, err := s.View(ctx, strings.ToUpper(hartleyGUID))
	if err != nil || !ok || v.MinAltitude != 30 || v.MinimumMinutes != 60 || v.MagLimit != 15 || v.UsableNights != 108 ||
		v.FirstUsable == nil || *v.FirstUsable != night12 || v.Brightest == nil || v.BrightestNight == nil || len(v.Nights) != bodies.SeasonNights {
		t.Fatalf("view %+v, %v, %v", v, ok, err)
	}
	if _, ok, err := s.View(ctx, k2GUID); ok || err != nil {
		t.Errorf("view of an inactive target: %v %v", ok, err)
	}
	if v.TypicalWindow == nil || *v.TypicalWindow != 2.17 {
		t.Errorf("161P typical window %v", v.TypicalWindow)
	}
}

func TestNightPreviewByDesignation(t *testing.T) {
	t.Parallel()
	f := newSeasonFake(t)
	s := nightService(t, nil, f, bodies.ModeOn)
	ctx := context.Background()
	v, err := s.Preview(ctx, " 161P ", "", 30, 90)
	if err != nil || v.Kind != bodies.KindComet || v.MagLimit != 15 || len(v.Nights) != bodies.SeasonNights || v.MinimumMinutes != 90 {
		t.Fatalf("161P preview %+v, %v", v, err)
	}
	if v.TypicalWindow == nil || *v.TypicalWindow != 2.17 || v.Nights[0].DarkHours != 4.83 || v.UsableNights == 0 {
		t.Errorf("161P typical window %v, tonight %+v, usable %d", v.TypicalWindow, v.Nights[0], v.UsableNights)
	}
	k, err := s.Preview(ctx, k2, bodies.KindComet, 30, 90)
	if err != nil || k.TypicalWindow != nil || k.UsableNights != 0 || k.BrightNights != 0 {
		t.Errorf("C/2017 K2 preview: typical %v usable %d bright %d, %v", k.TypicalWindow, k.UsableNights, k.BrightNights, err)
	}
	a, err := s.Preview(ctx, "4", bodies.KindAsteroid, 30, 90)
	if err != nil || a.MagLimit != 12 || a.TypicalWindow == nil || *a.TypicalWindow < 3 {
		t.Errorf("Vesta preview: limit %v typical %v, %v", a.MagLimit, a.TypicalWindow, err)
	}
	if _, err := s.Preview(ctx, "C/2099 A1", bodies.KindComet, 30, 90); !errors.Is(err, horizons.ErrUnknownBody) {
		t.Errorf("unknown body: %v", err)
	}
	if _, err := nightService(t, nil, f, bodies.ModeOff).Preview(ctx, hartley, "", 30, 90); !errors.Is(err, bodies.ErrNightsOff) {
		t.Errorf("off: %v", err)
	}
}

func TestNightServiceIsInertWithoutTableOrInDryRun(t *testing.T) {
	t.Parallel()
	f := newSeasonFake(t)
	ctx := context.Background()

	noTable := seasonScheduler(t, false)
	res, err := nightService(t, noTable, f, bodies.ModeOn).Refresh(ctx)
	if err != nil || res.Skipped != "no ts_body_night table" || res.Rows != 3*(bodies.SeasonNights+2) {
		t.Fatalf("no table %+v, %v", res, err)
	}
	if noTable.Migrator().HasTable(bodies.NightTable) {
		t.Error("the stacker created ts_body_night")
	}

	sched := seasonScheduler(t, true)
	res, err = nightService(t, sched, f, bodies.ModeDryRun).Refresh(ctx)
	if err != nil || res.Changed != 3*(bodies.SeasonNights+2) || res.PublishedAt != nil {
		t.Fatalf("dry run %+v, %v", res, err)
	}
	if n := nightCount(t, sched, ""); n != 0 {
		t.Errorf("dry run wrote %d rows", n)
	}

	before := f.total()
	res, err = nightService(t, sched, f, bodies.ModeOff).Refresh(ctx)
	if err != nil || res.Skipped != bodies.SkipOff || f.total() != before {
		t.Fatalf("off %+v, %v", res, err)
	}
}
