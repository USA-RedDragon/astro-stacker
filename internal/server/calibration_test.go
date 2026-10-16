package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/calpublish"
	"github.com/USA-RedDragon/astro-stacker/internal/calreq"
	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/testdb"
	"github.com/gin-gonic/gin"
)

const (
	calNight  = "2026-10-09"
	calObject = "M 31"
	biasType  = "BIAS"
	flatType  = "FLAT"
)

func calibrationEngine(t *testing.T) *gin.Engine {
	t.Helper()
	appStore := memStore(t)
	db := appStore.DB()
	if err := db.AutoMigrate(&app.Frame{}, &app.CalibrationRequest{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	night, _ := time.Parse(time.DateOnly, calNight)
	n := 0
	add := func(f app.Frame, count int, start time.Time) {
		for i := range count {
			n++
			at := start.Add(time.Duration(i) * time.Minute)
			f.ID, f.Key, f.LastModified, f.DateObs = 0, fmt.Sprintf("f%d", n), now, &at
			if f.Night == nil || f.Type != lightType {
				d := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
				f.Night = &d
			}
			if err := db.Create(&f).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	exp, gain, offset, temp, bin := 300.0, 100.0, 50.0, -10.0, 1.0
	for _, filter := range []string{filterRed, "Green"} {
		add(app.Frame{Type: lightType, Object: calObject, Filter: filter, Exposure: &exp, Gain: &gain, Offset: &offset, SetTemp: &temp, BinX: &bin,
			Night: &night}, 3, now.Add(-12*time.Hour))
	}
	oldBias := time.Date(2025, 2, 5, 18, 0, 0, 0, time.UTC)
	add(app.Frame{Type: biasType, Gain: &gain, Offset: &offset, BinX: &bin}, 30, oldBias)
	created := now.Add(-2 * time.Hour)
	id := 12
	ctx := context.Background()
	for _, typ := range []string{app.CalibrationFlats, app.CalibrationBias, app.CalibrationDarks} {
		if _, err := calreq.Open(ctx, db, calreq.Payload{Type: typ, Object: calObject, TargetID: &id, Night: calNight}, "req-"+typ, "jacob", created); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := calreq.Cancel(ctx, db, calreq.Payload{Type: app.CalibrationDarks, Object: calObject, Night: calNight}, "jacob", created.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	add(app.Frame{Type: flatType, Object: calObject, Filter: filterRed}, 3, now.Add(-time.Hour))
	add(app.Frame{Type: biasType, Gain: &gain, Offset: &offset, BinX: &bin}, 12, now.Add(-time.Hour))
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	x := Extras{Now: func() time.Time { return now }, DarkNeed: &darkneed.Publisher{Mode: darkneed.PublishOff},
		BiasNeed: calpublish.NewBias(nil, nil, darkneed.PublishOff), FlatRequests: calpublish.NewFlat(nil, nil, darkneed.PublishOn)}
	applyCalibrationRoutes(r.Group("/api/v1"), x)
	applyDarkBacklogRoutes(r.Group("/api/v1"), x.DarkNeed, x.Now)
	return r
}

func getJSON(t *testing.T, r *gin.Engine, path string, out any) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func TestCalibrationRequestsRoute(t *testing.T) {
	t.Parallel()
	r := calibrationEngine(t)
	var all []map[string]any
	getJSON(t, r, "/api/v1/calibration/requests", &all)
	if len(all) != 3 {
		t.Fatalf("requests %v", all)
	}
	byType := map[string]map[string]any{}
	for _, v := range all {
		typ, _ := v["type"].(string)
		byType[typ] = v
	}
	flats := byType[app.CalibrationFlats]
	items, _ := flats["items"].([]any)
	if flats["status"] != app.CalibrationOpen || flats["night"] != calNight || flats["target_id"] != 12.0 || len(items) != 2 || flats["basis"] == "" {
		t.Fatalf("flats %v", flats)
	}
	green, _ := items[0].(map[string]any)
	red, _ := items[1].(map[string]any)
	if red["key"] != filterRed || red["label"] != "Red flats" || red["frames_have"] != 3.0 || red["frames_needed"] != 3.0 || red["done"] != true ||
		green["done"] != false {
		t.Errorf("flat items %v", items)
	}
	pub, _ := flats["publish"].(map[string]any)
	if pub["table"] != calpublish.FlatTable || pub["mode"] != darkneed.PublishOn {
		t.Errorf("flats publish %v", pub)
	}
	bias := byType[app.CalibrationBias]
	bitems, _ := bias["items"].([]any)
	bi, _ := bitems[0].(map[string]any)
	if len(bitems) != 1 || bi["key"] != "bias|100|50|1" || bi["frames_have"] != 12.0 || bi["frames_needed"] != 30.0 {
		t.Errorf("bias items %v", bitems)
	}
	darks := byType[app.CalibrationDarks]
	if darks["status"] != app.CalibrationCancelled || darks["cancelled_by"] != "jacob" || darks["done_at"] != nil {
		t.Errorf("darks %v", darks)
	}
	var open []map[string]any
	getJSON(t, r, "/api/v1/calibration/requests?status=open&object=M%2031", &open)
	if len(open) != 2 {
		t.Errorf("open %v", open)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/calibration/requests?status=maybe", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad status: %d", w.Code)
	}
}

func TestBiasBacklogRoute(t *testing.T) {
	t.Parallel()
	r := calibrationEngine(t)
	var b map[string]any
	getJSON(t, r, "/api/v1/coverage/bias-backlog", &b)
	combos, _ := b["combos"].([]any)
	if len(combos) != 1 || b["frames_needed"] != 30.0 || b["max_age_days"] != nil || b["age_basis"] == "" || b["session_gap_hours"] != 36.0 {
		t.Fatalf("backlog %v", b)
	}
	c, _ := combos[0].(map[string]any)
	reqs, _ := c["requests"].([]any)
	if c["combo_key"] != "bias|100|50|1" || c["priority"] != 0.0 || c["lights_stale"] != nil || c["frames_have"] != 12.0 ||
		c["current_bias_night"] != "2026-10-10" || len(reqs) != 1 {
		t.Errorf("combo %v", c)
	}
	pub, _ := b["publish"].(map[string]any)
	if pub["mode"] != darkneed.PublishOff || pub["skipped"] != darkneed.SkipOff {
		t.Errorf("publish %v", pub)
	}
	var d map[string]any
	getJSON(t, r, "/api/v1/coverage/dark-backlog", &d)
	dcombos, _ := d["combos"].([]any)
	dc, _ := dcombos[0].(map[string]any)
	if dr, ok := dc["requests"].([]any); !ok || len(dr) != 0 {
		t.Errorf("dark combo requests %v", dc["requests"])
	}
}

// Darks and bias carry their ingest check verdicts; flats have no check, so
// their runs carry no clean, leak or unchecked counts at all.
func TestRecentCalibrationRoute(t *testing.T) {
	t.Parallel()
	checkRecentCalibration(t, memStore(t))
}

func TestRecentCalibrationRouteOnPostgres(t *testing.T) {
	t.Parallel()
	checkRecentCalibration(t, testStore{testdb.Postgres(t, "stacker", "stacker")})
}

func checkRecentCalibration(t *testing.T, appStore testStore) {
	t.Helper()
	db := appStore.DB()
	if err := db.AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	n := 0
	add := func(f app.Frame, at time.Time, check string, leak bool) {
		n++
		f.Key, f.LastModified, f.DateObs, f.IndexedAt = fmt.Sprintf("f%d", n), now, &at, at.Add(time.Minute)
		if check != "" {
			f.CalCheck = &check
		}
		if leak {
			v := 9.5
			f.LightLeak = &v
		}
		if err := db.Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := func(v float64) *float64 { return &v }
	dark := func(exp float64) app.Frame {
		return app.Frame{Type: "DARK", Exposure: p(exp), Gain: p(0), Offset: p(50), SetTemp: p(-10), BinX: p(1)}
	}
	start := now.Add(-6 * time.Hour)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	add(dark(300), now.Add(-30*time.Hour), "clean", false)
	add(dark(300), at(0), "clean", false)
	add(dark(300), at(5), "clean", false)
	add(dark(300), at(10), "leak", false)
	add(dark(300), at(15), "", true)
	add(dark(300), at(20), "", false)
	add(dark(120), at(26), "clean", false)
	bad := dark(120)
	msg := "unreadable"
	bad.IndexError = &msg
	add(bad, at(27), "", false)
	add(dark(300), at(70), "unchecked", false)
	for i := range 3 {
		add(app.Frame{Type: flatType, Object: calObject, Filter: filterRed, Exposure: p(2.5), Gain: p(0), Offset: p(50), BinX: p(1)}, at(100+i), "", false)
	}
	add(app.Frame{Type: biasType, Exposure: p(0), Gain: p(0), Offset: p(50), BinX: p(1)}, at(110), "clean", false)
	add(app.Frame{Type: "LIGHT", Object: calObject, Filter: filterRed, Exposure: p(300)}, at(200), "", false)

	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyCalibrationRoutes(r.Group("/api/v1"), Extras{Now: func() time.Time { return now }})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/scheduler/calibration/recent", nil))
	want := `{"from":"2026-10-14T12:00:00Z","to":"2026-10-15T12:00:00Z","newest_indexed":"2026-10-15T07:51:00Z","runs":[` +
		`{"kind":"bias","started_at":"2026-10-15T07:50:00Z","ended_at":"2026-10-15T07:50:00Z","exposure":0,"gain":0,"offset":50,"binning":1,"frames":1,"clean":1,"leak":0,"unchecked":0},` +
		`{"kind":"flats","started_at":"2026-10-15T07:40:00Z","ended_at":"2026-10-15T07:42:02.5Z","target_name":"M 31","filter":"Red","exposure":2.5,"gain":0,"offset":50,"binning":1,"frames":3},` +
		`{"kind":"darks","started_at":"2026-10-15T07:10:00Z","ended_at":"2026-10-15T07:15:00Z","exposure":300,"gain":0,"offset":50,"binning":1,"set_temp":-10,"frames":1,"clean":0,"leak":0,"unchecked":1},` +
		`{"kind":"darks","started_at":"2026-10-15T06:26:00Z","ended_at":"2026-10-15T06:28:00Z","exposure":120,"gain":0,"offset":50,"binning":1,"set_temp":-10,"frames":1,"clean":1,"leak":0,"unchecked":0},` +
		`{"kind":"darks","started_at":"2026-10-15T06:00:00Z","ended_at":"2026-10-15T06:25:00Z","exposure":300,"gain":0,"offset":50,"binning":1,"set_temp":-10,"frames":5,"clean":2,"leak":2,"unchecked":1}]}`
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Fatalf("%d\n got %s\nwant %s", w.Code, w.Body, want)
	}
	for _, q := range []string{"0", "169", "x"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/scheduler/calibration/recent?hours="+q, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("hours=%s: %d", q, w.Code)
		}
	}
	var wide coverage.RecentCalibration
	getJSON(t, r, "/api/v1/scheduler/calibration/recent?hours=48", &wide)
	if len(wide.Runs) != 6 || wide.Runs[5].Frames != 1 {
		t.Errorf("48 hours: runs %+v", wide.Runs)
	}
}
