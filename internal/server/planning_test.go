package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type testStore struct{ db *gorm.DB }

func (s testStore) WithContext(ctx context.Context) store.Store {
	return testStore{s.db.WithContext(ctx)}
}
func (s testStore) DB() *gorm.DB { return s.db }

func memStore(t *testing.T) testStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	return testStore{db}
}

func planningEngine(t *testing.T) *gin.Engine {
	t.Helper()
	sched := memStore(t)
	appStore := memStore(t)
	if err := appStore.DB().AutoMigrate(&app.Stack{}, &app.GoalMeasurement{}, &app.FrameTarget{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`create table project ("Id" integer primary key, "profileId" text, name text, description text, state integer, priority integer, minimumtime integer, minimumaltitude real, "maximumAltitude" real, "isMosaic" integer, enablegrader integer, guid text, ditherevery integer, filterswitchfrequency integer, "flatsHandling" integer, smartexposureorder integer, meridianwindow integer, usecustomhorizon integer, horizonoffset real)`,
		`create table target ("Id" integer primary key, name text, active integer, ra real, "dec" real, rotation real, projectid integer, guid text)`,
		`create table exposuretemplate ("Id" integer primary key, "profileId" text, name text, filtername text, gain integer, "offset" integer, bin integer, twilightlevel integer, moonavoidanceenabled integer, moonavoidanceseparation real, moonavoidancewidth integer, maximumhumidity real, defaultexposure real, moonrelaxscale real, moondownenabled integer, ditherevery integer, guid text)`,
		`create table exposureplan ("Id" integer primary key, "profileId" text, exposure real, desired integer, acquired integer, accepted integer, targetid integer, "exposureTemplateId" integer, enabled integer, guid text)`,
		`create table ruleweight ("Id" integer primary key, name text, weight real, projectid integer)`,
		`create table acquiredimage ("Id" integer primary key, "projectId" integer, "targetId" integer, acquireddate integer)`,
		`insert into project values (7,'p','Garlic Nebula',null,1,1,60,20,70,0,1,'pg7',2,1,100,1,30,1,1.5)`,
		`insert into target values (20,'Garlic Nebula',1,23.987,62.44,0,7,'g20')`,
		`insert into exposuretemplate values (1,'p','H-a','H-a',100,10,1,2,1,45,7,85,600,0,0,1,'t1'),(2,'p','O-III','O-III',100,10,1,2,1,45,7,85,600,0,0,1,'t2')`,
		`insert into exposureplan values (50,'p',-1,100,64,64,20,1,1,'e50')`,
	} {
		if err := sched.DB().Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: sched}))
	applyPlanningRoutes(r.Group("/api/v1"), nil)
	return r
}

func get(t *testing.T, r *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestPlanningDraftsRoute(t *testing.T) {
	t.Parallel()
	r := planningEngine(t)
	code, out := get(t, r, http.MethodGet, "/api/v1/planning", "")
	projects, _ := out["projects"].([]any)
	frame, _ := out["frame"].(map[string]any)
	sets, _ := out["sets"].([]any)
	if code != http.StatusOK || len(projects) != 1 || frame["widthDeg"] != nil || frame["reason"] == nil || len(sets) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	setID, _ := sets[0].(map[string]any)["id"].(string)
	code, out = get(t, r, http.MethodPost, "/api/v1/planning/applyset/draft", `{"setId":"`+setID+`","mode":"add","targetIds":[20]}`)
	if code != http.StatusOK || out["payload"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	code, out = get(t, r, http.MethodPost, "/api/v1/planning/projects/draft", `{"name":"Gecko","setId":"`+setID+`","priority":"Normal","minimumTime":60,"minimumAltitude":15,"goal":{"kind":"snr","snr":10},"panels":[{"raHours":22.5,"dec":40.8,"rotation":0}]}`)
	if code != http.StatusOK || out["project"] == nil {
		t.Fatalf("%d %v", code, out)
	}
	code, _ = get(t, r, http.MethodPost, "/api/v1/planning/projects/draft", `{"name":"Gecko","setId":"lrgb","minimumTime":60,"panels":[{"raHours":22.5,"dec":40.8}]}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown set should be 422, got %d", code)
	}
}

func TestPlanningProjectSettingsRoute(t *testing.T) {
	t.Parallel()
	r := planningEngine(t)
	want := map[string]any{"ditherEvery": 2.0, "filterSwitchFrequency": 1.0, "flatsHandling": 100.0, "smartExposureOrder": true, "meridianWindow": 30.0, "useCustomHorizon": true, "horizonOffset": 1.5, "maximumAltitude": 70.0, "grader": true}
	code, out := get(t, r, http.MethodGet, "/api/v1/planning", "")
	projects, _ := out["projects"].([]any)
	if code != http.StatusOK || len(projects) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	p, _ := projects[0].(map[string]any)
	for k, v := range want {
		if p[k] != v {
			t.Fatalf("%s = %v, want %v", k, p[k], v)
		}
	}
	defaults, _ := out["defaults"].(map[string]any)
	ps, _ := defaults["projectSettings"].(map[string]any)
	wantDefaults := map[string]any{"ditherEvery": 2.0, "filterSwitchFrequency": 1.0, "flatsHandling": 100.0, "smartExposureOrder": true, "meridianWindow": 30.0, "useCustomHorizon": true, "horizonOffset": 1.5, "maximumAltitude": 70.0, "enableGrader": true, "basis": "profile", "projects": 1.0, "profileId": "p"}
	for k, v := range wantDefaults {
		if ps[k] != v {
			t.Fatalf("defaults %s = %v, want %v (%v)", k, ps[k], v, ps)
		}
	}
	code, out = get(t, r, http.MethodGet, "/api/v1/planning/projects/7", "")
	one, _ := out["project"].(map[string]any)
	if code != http.StatusOK || one["flatsHandling"] != 100.0 || one["ditherEvery"] != 2.0 || one["maximumAltitude"] != 70.0 {
		t.Fatalf("%d %v", code, out)
	}
	draft := `{"name":"Gecko","plans":[{"templateId":1,"exposure":300}],"desired":40,"minimumTime":60,"minimumAltitude":15,"panels":[{"raHours":22.5,"dec":40.8}]%s}`
	code, out = get(t, r, http.MethodPost, "/api/v1/planning/projects/draft", fmt.Sprintf(draft, `,"ditherEvery":4,"maximumAltitude":0`))
	proj, _ := out["project"].(map[string]any)
	if code != http.StatusOK || proj["ditherevery"] != 4.0 || proj["flatshandling"] != 100.0 || proj["maximumaltitude"] != 0.0 || proj["enablegrader"] != true || proj["smartexposureorder"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = get(t, r, http.MethodPost, "/api/v1/planning/projects/draft", fmt.Sprintf(draft, `,"flatsHandling":150`)); code != http.StatusUnprocessableEntity {
		t.Fatalf("flats 150 should be 422, got %d %v", code, out)
	}
}
