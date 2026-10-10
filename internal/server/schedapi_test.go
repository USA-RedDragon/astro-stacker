package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/events"
	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	configulator "github.com/USA-RedDragon/configulator/v2"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	pathPreview  = "/api/v1/scheduler/preview"
	pathProjects = "/api/v1/scheduler/projects"
	pathSubs     = "/api/v1/scheduler/tonight-subs"
	targetP15    = "IC 1318 Panel 15"
	sadr         = "Sadr Region"
	light        = "LIGHT"
)

type dbStore struct{ db *gorm.DB }

func (s dbStore) WithContext(context.Context) store.Store { return s }
func (s dbStore) DB() *gorm.DB                            { return s.db }

func memDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%s_%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), name, time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func exec(t *testing.T, db *gorm.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func newHandler(t *testing.T, sched, appDB *gorm.DB, x server.Extras) http.Handler {
	t.Helper()
	cfg, err := configulator.New(config.ConfigSchema()).Default()
	if err != nil {
		t.Fatal(err)
	}
	s := server.NewServer(&cfg, dbStore{appDB}, dbStore{sched}, nil, events.NewBroker(), nil, "test", x)
	return s.Handler()
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

type fakePreviews struct {
	mu     sync.Mutex
	calls  int
	starts []*time.Time
	posted []observatory.PreviewRequest
	err    error
}

func (f *fakePreviews) Preview(_ context.Context, start *time.Time) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.starts = append(f.starts, start)
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(fmt.Sprintf(`{"call":%d}`, f.calls)), nil
}

func (f *fakePreviews) PreviewWith(_ context.Context, req observatory.PreviewRequest) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, req)
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(`{"what_if":true}`), nil
}

func TestPreviewWithoutPlugin(t *testing.T) {
	t.Parallel()
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{})
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		code, body := do(t, h, m, pathPreview, "{}")
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), `"error"`) {
			t.Errorf("%s: %d %s", m, code, body)
		}
	}
	code, body := do(t, h, http.MethodGet, "/api/v1/scheduler/status", "")
	if code != http.StatusOK || !strings.Contains(string(body), "unconfigured") {
		t.Errorf("status: %d %s", code, body)
	}
}

func TestPreviewProxyAndCache(t *testing.T) {
	t.Parallel()
	f := &fakePreviews{}
	now := time.Date(2026, 10, 10, 6, 52, 0, 0, time.UTC)
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{Previews: f, Now: func() time.Time { return now }})

	code, body := do(t, h, http.MethodGet, pathPreview, "")
	if code != http.StatusOK || string(body) != `{"call":1}` {
		t.Fatalf("first: %d %s", code, body)
	}
	if _, body = do(t, h, http.MethodGet, pathPreview+"?night=tonight", ""); string(body) != `{"call":1}` {
		t.Fatalf("cached: %s", body)
	}
	if _, body = do(t, h, http.MethodGet, pathPreview+"?fresh=1", ""); string(body) != `{"call":2}` {
		t.Fatalf("fresh: %s", body)
	}
	now = now.Add(61 * time.Second)
	if _, body = do(t, h, http.MethodGet, pathPreview, ""); string(body) != `{"call":3}` {
		t.Fatalf("expired: %s", body)
	}
	if _, body = do(t, h, http.MethodGet, pathPreview+"?night=tomorrow", ""); string(body) != `{"call":4}` {
		t.Fatalf("tomorrow: %s", body)
	}
	if code, _ = do(t, h, http.MethodGet, pathPreview+"?night=yesterday", ""); code != http.StatusBadRequest {
		t.Fatalf("bad night: %d", code)
	}
	f.mu.Lock()
	if f.starts[0] != nil || f.starts[3] == nil || f.starts[3].Format(time.RFC3339) != "2026-10-10T17:00:00Z" {
		t.Errorf("starts %v %v", f.starts[0], f.starts[3])
	}
	f.mu.Unlock()

	code, body = do(t, h, http.MethodPost, pathPreview, `{"overrides":[{"entity":"project","id":5,"field":"priority","value":2}]}`)
	if code != http.StatusOK || string(body) != `{"what_if":true}` {
		t.Fatalf("post: %d %s", code, body)
	}
	f.mu.Lock()
	if len(f.posted) != 1 || len(f.posted[0].Overrides) != 1 || f.posted[0].Overrides[0].ID != 5 || string(f.posted[0].Overrides[0].Value) != "2" {
		t.Errorf("posted %+v", f.posted)
	}
	f.err = fmt.Errorf("dial: %w", schedcmd.ErrUnreachable)
	f.mu.Unlock()
	if code, _ = do(t, h, http.MethodPost, pathPreview, `{}`); code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable post: %d", code)
	}
	if code, _ = do(t, h, http.MethodGet, pathPreview+"?fresh=1", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable get: %d", code)
	}
	f.mu.Lock()
	f.err = errors.New("boom")
	f.mu.Unlock()
	if code, _ = do(t, h, http.MethodGet, pathPreview+"?fresh=1", ""); code != http.StatusBadGateway {
		t.Fatalf("broken: %d", code)
	}
}

func schedulerTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec(t, db,
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "isMosaic" integer, enablegrader integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, defaultexposure real, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer, filtername text, "gradingStatus" integer, metadata text, rejectreason text, "profileId" text, "exposureId" integer, guid text)`,
		`insert into project values (5,'p','Sadr Region',null,1,2,30,10,1,1,'pg5'),(7,'p','Garlic Nebula',null,2,1,60,20,0,1,'pg7'),(9,'old','Other profile',null,1,1,60,20,0,1,'pg9')`,
		`insert into target values (13,'IC 1318 Panel 15',1,20.04,35.98,0,5,'tg13'),(14,'IC 1318 Panel 12',0,20.1,36.2,0,5,'tg14'),(20,'Garlic Nebula',1,23.98,62.44,0,7,'tg20')`,
		`insert into exposuretemplate values (1,'p','Red 600','R',600,'t1'),(2,'p','Ha 300','Ha',300,'t2')`,
		`insert into exposureplan values (40,'p',-1,20,6,4,13,1,1,'e40'),(41,'p',120,10,0,0,13,2,0,'e41'),(50,'p',-1,30,30,30,20,2,1,'e50')`,
	)
}

func checkSadr(t *testing.T, p server.SchedProject, last time.Time) {
	t.Helper()
	if p.Name != sadr || !p.IsMosaic || p.Priority != 2 || p.State != 1 || p.MinimumTime != 30 || len(p.Targets) != 2 || p.LastImage == nil || !p.LastImage.Equal(last) {
		t.Fatalf("project %+v", p)
	}
	tg := p.Targets[0]
	if tg.Name != targetP15 || !tg.Active || tg.RA == nil || *tg.RA != 20.04 || len(tg.Plans) != 2 {
		t.Fatalf("target %+v", tg)
	}
	if pl := tg.Plans[0]; pl.Filter != "R" || pl.Exposure != -1 || pl.DefaultExposure != 600 || pl.Desired != 20 || pl.Accepted != 4 || !pl.Enabled {
		t.Fatalf("plan %+v", pl)
	}
	if pl := tg.Plans[1]; pl.Filter != "Ha" || pl.Exposure != 120 || pl.Enabled {
		t.Fatalf("plan %+v", pl)
	}
	if p.Targets[1].Active || len(p.Targets[1].Plans) != 0 {
		t.Fatalf("panel 12 %+v", p.Targets[1])
	}
}

func TestSchedulerProjects(t *testing.T) {
	t.Parallel()
	empty := newHandler(t, memDB(t, "s0"), memDB(t, "a0"), server.Extras{})
	if code, body := do(t, empty, http.MethodGet, pathProjects, ""); code != http.StatusOK || string(body) != "[]" {
		t.Fatalf("no tables: %d %s", code, body)
	}

	sched := memDB(t, "s")
	schedulerTables(t, sched)
	last := time.Date(2026, 10, 10, 6, 40, 0, 0, time.UTC)
	exec(t, sched, fmt.Sprintf(`insert into acquiredimage values (1,5,13,%d,'R',1,'{}',null,'p',40,'a1'),(2,7,20,%d,'Ha',1,'{}',null,'p',50,'a2')`,
		last.Unix(), last.Unix()*10000000+621355968000000000-3600*10000000))
	h := newHandler(t, sched, memDB(t, "a"), server.Extras{})
	code, body := do(t, h, http.MethodGet, pathProjects, "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got []server.SchedProject
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d projects, want the 2 of the most common profile: %s", len(got), body)
	}
	checkSadr(t, got[0], last)
	if g := got[1]; g.LastImage == nil || !g.LastImage.Equal(last.Add(-time.Hour)) {
		t.Fatalf("ticks date: %+v", g.LastImage)
	}
}

func checkTonight(t *testing.T, got server.TonightSubs, at func(int) time.Time) {
	t.Helper()
	a, b, c := got.Subs[0], got.Subs[1], got.Subs[2]
	if a.Target != targetP15 || a.Project != sadr || a.File != "a.fits" || a.HFR == nil || *a.HFR != 2.3 || a.GuidingRMS == nil || *a.GuidingRMS != 0.6 ||
		a.GuidingRMSDec != nil || a.Verdict != app.StackStatusAdded || a.Score == nil || *a.Score != 0.68 || a.Grading != "accepted" || a.Gain == nil {
		t.Fatalf("a %+v", a)
	}
	if !b.Time.Equal(at(6)) || b.Verdict != app.StackStatusLowScore || b.Grading != "rejected" {
		t.Fatalf("b %+v", b)
	}
	if c.Verdict != "" || c.Grading != "pending" || c.Target != "Garlic Nebula" {
		t.Fatalf("c %+v", c)
	}
	if got.Latest == nil || got.Latest.ID != 3 || got.Latest.PreviewURL != "" {
		t.Fatalf("latest %+v", got.Latest)
	}
}

func tonightFixture(t *testing.T) (*gorm.DB, *gorm.DB, func(int) time.Time) {
	t.Helper()
	sched := memDB(t, "s")
	schedulerTables(t, sched)
	appDB := memDB(t, "a")
	if err := appDB.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 10, 9, 17, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return night.Add(time.Duration(h) * time.Hour) }
	meta := func(file string, hfr float64) string {
		return fmt.Sprintf(`{"FileName":"C:\\NINA\\%s","HFR":%v,"GuidingRMSArcSec":0.6,"GuidingRMSRAArcSec":0.39,"GuidingRMSDECArcSec":"NaN","ExposureDuration":600}`, file, hfr)
	}
	exec(t, sched, fmt.Sprintf(`insert into acquiredimage values
		(1,5,13,%d,'R',1,'%s',null,'p',40,'a1'),
		(2,5,13,%d,'R',2,'%s',null,'p',40,'a2'),
		(3,7,20,%d,'Ha',0,'%s',null,'p',50,'a3'),
		(4,7,20,%d,'Ha',1,'{}',null,'p',50,'a4')`,
		at(5).Unix(), meta("a.fits", 2.3),
		at(6).Unix()*10000000+621355968000000000, meta("b.fits", 3.4),
		at(7).Unix(), meta("c.fits", 2.1),
		at(-30).Unix()))
	exp := 600.0
	gain := 100.0
	dobs := at(5)
	preview := "previews/a.jpg"
	w, hgt := 6248, 4176
	frames := []app.Frame{
		{Key: "lights/IC 1318 Panel 15/a.fits", ETag: "1", Type: light, Object: targetP15, Filter: "Red", Exposure: &exp, Gain: &gain, DateObs: &dobs, PreviewKey: &preview,
			Width: &w, Height: &hgt},
		{Key: "lights/IC 1318 Panel 15/b.fits", ETag: "2", Type: light, Object: targetP15, Filter: "Red", Exposure: &exp, DateObs: &dobs},
		{Key: "lights/Other/c.fits", ETag: "3", Type: light, Object: "Other", Filter: "Ha", DateObs: &dobs},
	}
	if err := appDB.Create(&frames).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Create(&[]app.StackFrame{
		{FrameID: frames[0].ID, Status: app.StackStatusAdded, Score: 0.68, Weight: 204},
		{FrameID: frames[1].ID, Status: app.StackStatusLowScore, Score: 0.1},
	}).Error; err != nil {
		t.Fatal(err)
	}

	return sched, appDB, at
}

func TestTonightSubs(t *testing.T) {
	t.Parallel()
	sched, appDB, at := tonightFixture(t)
	h := newHandler(t, sched, appDB, server.Extras{Now: func() time.Time { return at(9) }})
	code, body := do(t, h, http.MethodGet, pathSubs, "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got server.TonightSubs
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Subs) != 3 {
		t.Fatalf("got %d subs, want tonight's 3: %s", len(got.Subs), body)
	}
	checkTonight(t, got, at)
	if a := got.Subs[0]; a.Width == nil || *a.Width != 6248 || a.Height == nil || *a.Height != 4176 || got.Subs[1].Width != nil {
		t.Errorf("geometry %+v %+v", a, got.Subs[1])
	}
	if !strings.Contains(string(body), `"width":null`) {
		t.Errorf("unknown width not null: %s", body)
	}

	code, body = do(t, h, http.MethodGet, pathSubs+"?since="+at(6).Format(time.RFC3339), "")
	if err := json.Unmarshal(body, &got); code != http.StatusOK || err != nil || len(got.Subs) != 2 {
		t.Fatalf("since: %d %s", code, body)
	}
	if code, _ = do(t, h, http.MethodGet, pathSubs+"?since=yesterday", ""); code != http.StatusBadRequest {
		t.Fatalf("bad since: %d", code)
	}

	empty := newHandler(t, memDB(t, "s0"), memDB(t, "a0"), server.Extras{})
	if code, body = do(t, empty, http.MethodGet, pathSubs, ""); code != http.StatusOK || !strings.Contains(string(body), `"subs":[]`) {
		t.Fatalf("no tables: %d %s", code, body)
	}
}

func TestSchedulerStatusFromMonitor(t *testing.T) {
	t.Parallel()
	m := observatory.NewMonitor(observatory.NewClient("http://127.0.0.1:1", "x"), nil, nil)
	m.SetStatus(observatory.Status{State: "paused", Paused: true}, true)
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{Scheduler: m})
	code, body := do(t, h, http.MethodGet, "/api/v1/scheduler/status", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"reachable":"online"`) || !strings.Contains(string(body), `"state":"paused"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestTonightHFRLimits(t *testing.T) {
	t.Parallel()
	sched, appDB, at := tonightFixture(t)
	exec(t, sched, `create table profilepreference ("profileId" text, "enableGradeHFR" integer, "hfrSigmaFactor" real)`,
		`insert into profilepreference values ('p', 1, 2.0)`)
	for i, hfr := range []float64{2.0, 2.2, 2.4} {
		exec(t, sched, fmt.Sprintf(`insert into acquiredimage values (%d,5,13,%d,'R',1,'{"HFR":%v}',null,'p',40,'x%d')`, 10+i, at(-48).Unix(), hfr, i))
	}
	h := newHandler(t, sched, appDB, server.Extras{Now: func() time.Time { return at(9) }})
	code, body := do(t, h, http.MethodGet, pathSubs, "")
	var got server.TonightSubs
	if err := json.Unmarshal(body, &got); code != http.StatusOK || err != nil {
		t.Fatalf("%d %s", code, body)
	}
	if got.HFRSigma == nil || *got.HFRSigma != 2 || len(got.HFRLimits) != 1 {
		t.Fatalf("limits %s", body)
	}
	l := got.HFRLimits[0]
	if l.TargetID != 13 || l.Filter != "R" || l.Samples != 4 || math.Abs(l.Mean-2.225) > 1e-9 || math.Abs(l.Limit-(l.Mean+2*l.SD)) > 1e-9 {
		t.Fatalf("limit %+v", l)
	}
	exec(t, sched, `update profilepreference set "enableGradeHFR" = 0`)
	_, body = do(t, h, http.MethodGet, pathSubs, "")
	if strings.Contains(string(body), "hfr_sigma") || !strings.Contains(string(body), `"hfr_limits":[]`) {
		t.Fatalf("grader off: %s", body)
	}
}

func TestConditionsAndMoon(t *testing.T) {
	t.Parallel()
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{})
	code, body := do(t, h, http.MethodGet, "/api/v1/scheduler/conditions", "")
	if code != http.StatusOK || !strings.Contains(string(body), `"weather":{"source":"none"}`) {
		t.Fatalf("conditions %d %s", code, body)
	}
	if code, _ = do(t, h, http.MethodGet, "/api/v1/scheduler/moon?start=x&end=y", ""); code != http.StatusBadRequest {
		t.Fatalf("bad times %d", code)
	}
	if code, _ = do(t, h, http.MethodGet, "/api/v1/scheduler/moon?start=2026-10-09T22:00:00Z&end=2026-10-10T12:00:00Z", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("no site %d", code)
	}
}
