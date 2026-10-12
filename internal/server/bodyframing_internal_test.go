package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/horizons"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/skyevents"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func framingGet(t *testing.T, f *BodyFraming, path string) (int, map[string]any) {
	t.Helper()
	r := gin.New()
	applyBodyFramingRoutes(r.Group("/api/v1"), f)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func TestBodyFramingRoutes(t *testing.T) {
	t.Parallel()
	if code, _ := framingGet(t, nil, "/api/v1/bodies/track?designation=161P"); code != http.StatusServiceUnavailable {
		t.Errorf("off: %d", code)
	}
	fixture, err := os.ReadFile("../bodies/testdata/horizons-topo-161p-psang.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(fixture) }))
	defer srv.Close()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`create table project ("Id" integer primary key, name text, state integer, guid text)`,
		`create table target ("Id" integer primary key, name text, active integer, projectid integer, guid text)`,
		`insert into project values (1, 'Comets', 1, 'p1')`,
		`insert into target values (1, '161P', 1, 1, 'g-161p')`,
		bodies.TargetSchema,
		`insert into ts_target_body (target_guid, designation, rotation_deg, offset_frac) values ('g-161p', '161P', 84.06, -0.25)`,
	} {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&app.HorizonsCache{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	e := 200.0
	site := func(context.Context) (sky.Site, error) {
		return sky.Site{Latitude: 32, Longitude: -97, Elevation: &e}, nil
	}
	f := &BodyFraming{
		Tracker: &bodies.Tracker{Horizons: &horizons.Client{BaseURL: srv.URL, Cache: db, Now: func() time.Time { return now }}, Site: site, Now: func() time.Time { return now }},
		Sched:   db,
		Frame: func(context.Context) (*skyevents.FrameInfo, error) {
			return &skyevents.FrameInfo{Width: 3.32, Height: 2.22, Basis: "measured"}, nil
		},
	}
	if code, _ := framingGet(t, f, "/api/v1/bodies/track"); code != http.StatusBadRequest {
		t.Errorf("no designation: %d", code)
	}
	code, body := framingGet(t, f, "/api/v1/bodies/track?designation=161P&kind=comet")
	if code != http.StatusOK {
		t.Fatalf("track: %d %v", code, body)
	}
	track, _ := body["track"].(map[string]any)
	frame, _ := body["frame"].(map[string]any)
	if track == nil || track["angles"] != true || frame == nil || frame["width_deg"] != 3.32 || frame["height_deg"] != 2.22 {
		t.Fatalf("track body %v", body)
	}
	if best, _ := track["best"].(map[string]any); best == nil || best["tail_pa"] == nil || best["motion_pa"] == nil {
		t.Fatalf("best %v", track["best"])
	}
	f.Frame = func(context.Context) (*skyevents.FrameInfo, error) { return nil, errors.New("no frames") }
	if _, body := framingGet(t, f, "/api/v1/bodies/track?designation=161P"); body["frame"] != nil || body["frame_reason"] == nil {
		t.Errorf("unmeasured frame: %v", body)
	}
	if code, _ := framingGet(t, f, "/api/v1/bodies/target"); code != http.StatusBadRequest {
		t.Errorf("no guid: %d", code)
	}
	code, body = framingGet(t, f, "/api/v1/bodies/target?target_guid=g-161p")
	tgt, _ := body["target"].(map[string]any)
	if code != http.StatusOK || body["framing_columns"] != true || tgt == nil || tgt["rotation_deg"] != 84.06 || tgt["offset_frac"] != -0.25 {
		t.Fatalf("target: %d %v", code, body)
	}
	if _, body := framingGet(t, f, "/api/v1/bodies/target?target_guid=nope"); body["target"] != nil {
		t.Errorf("unknown target: %v", body)
	}
}
