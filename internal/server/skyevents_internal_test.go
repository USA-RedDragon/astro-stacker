package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/planning"
	"github.com/USA-RedDragon/astro-stacker/internal/rigsource"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

const windowTableSQL = `create table ts_target_window (target_guid text NOT NULL PRIMARY KEY, start_utc text NOT NULL, end_utc text NOT NULL,
	priority text NOT NULL DEFAULT 'normal', event_id text, ignore_moon integer NOT NULL DEFAULT 0, updated_at text)`

type skyEventsRig struct {
	tr    *sentCommands
	call  func(method, path, body string) (int, map[string]any)
	sched testStore
	app   testStore
	cmds  *schedcmd.Service
	svc   *skyevents.Service
}

func skyEventsEngine(t *testing.T, withWindows bool) skyEventsRig {
	t.Helper()
	return skyEventsEngineAt(t, withWindows, time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC))
}

func skyEventsEngineAt(t *testing.T, withWindows bool, now time.Time) skyEventsRig {
	t.Helper()
	tr := &sentCommands{}
	cmds := &schedcmd.Service{Transports: []schedcmd.Transport{tr}}
	r, sched, appStore := planningEngineWith(t, cmds)
	if err := appStore.DB().AutoMigrate(&schedcmd.Record{}, &app.GlobalRuleWeight{}, &app.SkyEventRun{}, &app.SkyEventChoice{}, &app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	cmds.Log, cmds.AppDB = schedcmd.NewGormLog(appStore.DB()), appStore.DB()
	n := 0
	cmds.NewID = func() string { n++; return fmt.Sprintf("cmd-%d", n) }
	stmts := []string{
		`insert into exposuretemplate values (3,'p','Lum 180','L',100,10,1,0,1,45,7,85,180,0,0,1,'t3')`,
		bodies.TargetSchema, bodies.Schema,
	}
	if withWindows {
		stmts = append(stmts, windowTableSQL)
	}
	for _, q := range stmts {
		if err := sched.DB().Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	zp := 21.5
	if err := appStore.DB().Create(&app.GoalMeasurement{Object: "M 45", Filter: "L", ZeroPoint: &zp, DepthSystem: goals.SystemGaiaG, ZeroPointStars: 40}).Error; err != nil {
		t.Fatal(err)
	}
	svc := newSkyEventsService(appStore, sched, cmds, now)
	if _, err := svc.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	applySkyEventRoutes(r.Group("/api/v1"), svc, nil)
	return skyEventsRig{tr: tr, sched: sched, app: appStore, cmds: cmds, svc: svc, call: func(method, path, body string) (int, map[string]any) { return get(t, r, method, path, body) }}
}

func newSkyEventsService(appStore, sched testStore, cmds *schedcmd.Service, now time.Time) *skyevents.Service {
	w, h, scale, hfr := 3.32, 2.22, 1.91, 4.0
	return &skyevents.Service{App: appStore.DB(), Sched: sched.DB(), Commands: cmds, Now: func() time.Time { return now },
		Site: func(context.Context) (sky.Site, error) { return sky.Site{Latitude: 32, Longitude: -100}, nil },
		Rig: func(context.Context) (rigsource.Rig, error) {
			return rigsource.Rig{WidthDeg: &w, HeightDeg: &h, Scale: &scale, TypicalHFR: &hfr}, nil
		},
		Catalog: sync.OnceValues(catalog.LoadEmbedded),
		Snapshot: func(ctx context.Context) (*planning.Snapshot, error) {
			return LoadPlanningSnapshot(ctx, appStore.DB(), sched.DB(), nil)
		}}
}

func eventID(t *testing.T, out map[string]any, title string) string {
	t.Helper()
	evs, _ := out["events"].([]any)
	for _, e := range evs {
		m, _ := e.(map[string]any)
		if m["title"] == title {
			id, _ := m["id"].(string)
			return id
		}
	}
	t.Fatalf("no %s in %d events", title, len(evs))
	return ""
}

func sentPayload(t *testing.T, tr *sentCommands, i int, kind schedcmd.Kind, v any) {
	t.Helper()
	if len(tr.sent) <= i || tr.sent[i].Kind != kind {
		t.Fatalf("command %d is not %s: %+v", i, kind, tr.sent)
	}
	if err := json.Unmarshal(tr.sent[i].Payload, v); err != nil {
		t.Fatal(err)
	}
}

func TestSkyEventsWaitForThePluginWindows(t *testing.T) {
	t.Parallel()
	rig := skyEventsEngine(t, false)
	code, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	p, _ := out["planning"].(map[string]any)
	if code != http.StatusOK || out["status"] != skyevents.StatusReady || p["available"] != false || p["reason"] == nil {
		t.Fatalf("%d %v", code, p)
	}
	id := eventID(t, out, "Mars with M 44 Beehive")
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{"priority":"high"}`); code != http.StatusConflict {
		t.Fatalf("plan without the plugin table: %d %v", code, out)
	}
	if len(rig.tr.sent) != 0 {
		t.Fatalf("commands were sent: %+v", rig.tr.sent)
	}
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/priority", `{"priority":"high"}`); code != http.StatusOK || out["command"] != nil {
		t.Fatalf("priority before planning: %d %v", code, out)
	}
	_, out = rig.call(http.MethodGet, "/api/v1/sky-events?months=1", "")
	evs, _ := out["events"].([]any)
	first, _ := evs[0].(map[string]any)
	if out["months"] != 1.0 || first["id"] != id || first["priority"] != skyevents.PriorityHigh || first["plan"] != nil {
		t.Fatalf("stored priority not shown: %v", first)
	}
	for _, path := range []string{"/api/v1/sky-events?months=0", "/api/v1/sky-events?months=25", "/api/v1/sky-events?months=x"} {
		if code, _ := rig.call(http.MethodGet, path, ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code, _ := rig.call(http.MethodPost, "/api/v1/sky-events/nope/priority", `{"priority":"high"}`); code != http.StatusNotFound {
		t.Errorf("unknown event: %d", code)
	}
}

func TestSkyEventPlanSendsAWindowedProject(t *testing.T) {
	t.Parallel()
	rig := skyEventsEngine(t, true)
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	id := eventID(t, out, "Mars with M 44 Beehive")
	code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{"priority":"high"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	var p schedcmd.ProjectCreatePayload
	sentPayload(t, rig.tr, 0, schedcmd.KindProjectCreate, &p)
	tg := p.Targets[0]
	if p.Project.Priority != 2 || p.Project.MinimumAltitude != skyevents.DefaultMinAlt || tg.Window == nil || tg.Window.Priority != schedcmd.EventPriorityHigh ||
		tg.Window.EventID != id || tg.Window.IgnoreMoon || tg.Designation != "" || tg.BodyKind != "" || len(p.Goals) != 0 {
		t.Fatalf("payload %+v target %+v", p.Project, tg)
	}
	if len(tg.Plans) != 2 || tg.Plans[0].TemplateID != 3 || tg.Plans[0].Exposure >= 1 || tg.Plans[1].Exposure != 180 || tg.Plans[0].Desired < 1 {
		t.Fatalf("plans %+v", tg.Plans)
	}
	ev, _ := out["event"].(map[string]any)
	plan, _ := ev["plan"].(map[string]any)
	if plan["state"] != string(schedcmd.StatusQueued) || plan["target_guid"] != tg.GUID || ev["priority"] != skyevents.PriorityHigh {
		t.Fatalf("event after plan: %v", ev)
	}
	if code, _ := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{"priority":"normal"}`); code != http.StatusConflict {
		t.Errorf("planning twice: %d", code)
	}
	code, out = rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/priority", `{"priority":"normal"}`)
	if code != http.StatusOK || out["command"] == nil {
		t.Fatalf("priority: %d %v", code, out)
	}
	var w schedcmd.TargetWindowPayload
	sentPayload(t, rig.tr, 1, schedcmd.KindTargetWindow, &w)
	if w.TargetGUID != tg.GUID || w.Window.Priority != schedcmd.EventPriorityNormal || w.Before.Priority != schedcmd.EventPriorityHigh || !w.Window.Start.Equal(tg.Window.Start) {
		t.Fatalf("target.window %+v", w)
	}
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{"priority":"urgent"}`); code != http.StatusBadRequest {
		t.Errorf("bad priority: %d %v", code, out)
	}
}

func TestSkyEventTwoBodiesFollowAPublishedCentre(t *testing.T) {
	t.Parallel()
	rig := skyEventsEngine(t, true)
	_, out := rig.call(http.MethodGet, "/api/v1/sky-events", "")
	id := eventID(t, out, "Mars with Jupiter")
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{}`); code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	var p schedcmd.ProjectCreatePayload
	sentPayload(t, rig.tr, 0, schedcmd.KindProjectCreate, &p)
	tg := p.Targets[0]
	if tg.Designation != bodies.EventPrefix+id || tg.BodyKind != schedcmd.BodyKindEvent || tg.Window.Priority != schedcmd.EventPriorityNormal || len(tg.Plans) != 1 {
		t.Fatalf("target %+v", tg)
	}
	var rows []bodies.PositionRow
	if err := rig.sched.DB().Table(bodies.PositionTable).Order("at").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) < 100 {
		t.Fatalf("%d centre rows", len(rows))
	}
	for _, r := range rows {
		if r.TargetGUID != tg.GUID || r.Designation != tg.Designation || r.RARate == 0 {
			t.Fatalf("row %+v", r)
		}
	}
	if d := catalog.Separation(rows[0].RA, rows[0].Dec, rows[len(rows)-1].RA, rows[len(rows)-1].Dec); d < 1 {
		t.Errorf("the centre moved %.2f° over the window", d)
	}
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/unplan", ``); code != http.StatusOK {
		t.Fatalf("unplan %d %v", code, out)
	}
	var left int64
	if err := rig.sched.DB().Table(bodies.PositionTable).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d centre rows left after unplanning", left)
	}
	if code, _ := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/unplan", ``); code != http.StatusConflict {
		t.Errorf("unplanning twice: %d", code)
	}
	if code, out := rig.call(http.MethodPost, "/api/v1/sky-events/"+id+"/plan", `{"priority":"high"}`); code != http.StatusOK {
		t.Fatalf("planning again after unplan: %d %v", code, out)
	}
}
