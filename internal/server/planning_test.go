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
	"github.com/USA-RedDragon/astro-stacker/internal/ruleweights"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
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
	r, _, _ := planningEngineWith(t, nil)
	return r
}

func planningEngineWith(t *testing.T, cmds *schedcmd.Service) (*gin.Engine, testStore, testStore) {
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
	applyPlanningRoutes(r.Group("/api/v1"), nil, cmds)
	return r, sched, appStore
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
	code, out = get(t, r, http.MethodPost, "/api/v1/planning/projects/draft", `{"name":"Gecko","plans":[{"templateId":1}],"desired":40,"minimumTime":60,"panels":[{"raHours":22.5,"dec":40.8}]}`)
	targets, _ := out["targets"].([]any)
	if code != http.StatusOK || len(targets) != 1 {
		t.Fatalf("a plan without a sub length should draft: %d %v", code, out)
	}
	tgt, _ := targets[0].(map[string]any)
	plans, _ := tgt["plans"].([]any)
	var plan map[string]any
	if len(plans) == 1 {
		plan, _ = plans[0].(map[string]any)
	}
	if plan["exposure"] != -1.0 {
		t.Fatalf("a plan without a sub length should use the template length (-1): %v", plans)
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

type sentCommands struct{ sent []schedcmd.Envelope }

func (s *sentCommands) Name() string { return "test" }

func (s *sentCommands) Send(_ context.Context, e schedcmd.Envelope) (schedcmd.Result, error) {
	s.sent = append(s.sent, e)
	return schedcmd.Result{ID: e.ID, Status: schedcmd.StatusQueued}, nil
}

func (s *sentCommands) Cancel(_ context.Context, id string) (schedcmd.Result, error) {
	return schedcmd.Result{ID: id, Status: schedcmd.StatusCancelled}, nil
}

func weightsEngine(t *testing.T) (*sentCommands, func(method, path, body string) (int, map[string]any), testStore) {
	t.Helper()
	tr := &sentCommands{}
	cmds := &schedcmd.Service{Transports: []schedcmd.Transport{tr}}
	r, sched, appStore := planningEngineWith(t, cmds)
	if err := appStore.DB().AutoMigrate(&schedcmd.Record{}, &app.GlobalRuleWeight{}); err != nil {
		t.Fatal(err)
	}
	cmds.Log, cmds.AppDB = schedcmd.NewGormLog(appStore.DB()), appStore.DB()
	n := 0
	cmds.NewID = func() string { n++; return fmt.Sprintf("cmd-%d", n) }
	for _, q := range []string{
		`insert into project values (8,'p','Veil',null,1,1,60,20,70,1,1,'pg8',2,1,100,1,30,1,1.5)`,
		`insert into ruleweight (name, weight, projectid) values ('Novelty',35,7),('Novelty',5,8),('Rarity',75,7),('Rarity',20,8)`,
	} {
		if err := sched.DB().Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	return tr, func(method, path, body string) (int, map[string]any) { return get(t, r, method, path, body) }, appStore
}

func TestRuleWeightsRoutes(t *testing.T) {
	t.Parallel()
	tr, call, appStore := weightsEngine(t)
	code, out := call(http.MethodGet, "/api/v1/planning/rule-weights", "")
	weights, _ := out["weights"].(map[string]any)
	differing, _ := out["differing"].([]any)
	if code != http.StatusOK || out["basis"] != "projects" || weights["Novelty"] != 35.0 || weights["Novelty (mosaic)"] != 5.0 || len(differing) != 1 {
		t.Fatalf("%d %v", code, out)
	}

	code, out = call(http.MethodPost, "/api/v1/planning/rule-weights/apply", `{"weights":{"Rarity":101}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("out of range: %d %v", code, out)
	}
	code, _ = call(http.MethodPost, "/api/v1/planning/rule-weights/apply", `{"projectIds":[404]}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}

	code, out = call(http.MethodPost, "/api/v1/planning/rule-weights/apply", `{"weights":{"Rarity":50,"Novelty (mosaic)":5}}`)
	global, _ := out["global"].(map[string]any)
	projects, _ := out["projects"].([]any)
	if code != http.StatusAccepted || global["status"] != string(schedcmd.StatusSaved) || len(projects) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	saved, err := ruleweights.Load(context.Background(), appStore.DB())
	if err != nil || saved.Weights["Rarity"] != 50 || saved.Weights["Novelty"] != 35 || len(saved.Weights) != len(ruleweights.Keys()) {
		t.Fatalf("%+v %v", saved, err)
	}
	if len(tr.sent) != 2 {
		t.Fatalf("one ruleweight.edit per project, got %d", len(tr.sent))
	}
	for _, e := range tr.sent {
		var p schedcmd.RuleWeightEditPayload
		if e.Kind != schedcmd.KindRuleWeightEdit || json.Unmarshal(e.Payload, &p) != nil {
			t.Fatalf("%+v", e)
		}
		for _, c := range p.Changes {
			if c.Rule == "Rarity" && *c.After != 50 || c.Rule == ruleweights.NoveltyMosaic {
				t.Fatalf("%+v", p)
			}
			if c.Rule == "Novelty" && p.ProjectID == 8 && *c.After != 5 {
				t.Fatalf("the mosaic takes Novelty (mosaic): %+v", p)
			}
		}
	}
}

func TestRuleWeightsPreviewSavesNothing(t *testing.T) {
	t.Parallel()
	tr, call, appStore := weightsEngine(t)
	code, out := call(http.MethodPost, "/api/v1/planning/rule-weights/preview", `{"weights":{"Rarity":20}}`)
	weights, _ := out["weights"].(map[string]any)
	differing, _ := out["differing"].([]any)
	var first map[string]any
	if len(differing) > 0 {
		first, _ = differing[0].(map[string]any)
	}
	if code != http.StatusOK || weights["Rarity"] != 20.0 || len(differing) != 1 || first["id"] != 7.0 {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ = call(http.MethodPost, "/api/v1/planning/rule-weights/preview", `{"weights":{"Made Up":1}}`); code != http.StatusBadRequest {
		t.Fatalf("unknown rule: %d", code)
	}
	saved, err := ruleweights.Load(context.Background(), appStore.DB())
	if err != nil || len(saved.Weights) != 0 || len(tr.sent) != 0 {
		t.Fatalf("%+v %v %d", saved, err, len(tr.sent))
	}
}

func TestRuleWeightsResetOneProject(t *testing.T) {
	t.Parallel()
	tr, call, _ := weightsEngine(t)
	code, out := call(http.MethodPost, "/api/v1/planning/rule-weights/apply", `{"projectIds":[7]}`)
	projects, _ := out["projects"].([]any)
	if code != http.StatusAccepted || out["global"] != nil || len(projects) != 1 || len(tr.sent) != 1 {
		t.Fatalf("reset one project: %d %v", code, out)
	}
	var p schedcmd.RuleWeightEditPayload
	_ = json.Unmarshal(tr.sent[0].Payload, &p)
	if p.ProjectID != 7 || p.ProjectGUID != "pg7" || !strings.Contains(string(tr.sent[0].Payload), `"Rarity"`) {
		t.Fatalf("%s", tr.sent[0].Payload)
	}
}

func TestRuleWeightsApplyNeedsCommands(t *testing.T) {
	t.Parallel()
	r := planningEngine(t)
	if code, _ := get(t, r, http.MethodPost, "/api/v1/planning/rule-weights/apply", `{}`); code != http.StatusServiceUnavailable {
		t.Fatalf("%d", code)
	}
}

func TestPlanningFiltersDraftRoute(t *testing.T) {
	t.Parallel()
	r, sched, _ := planningEngineWith(t, nil)
	for _, q := range []string{`insert into target values (21,'Garlic Nebula 2',1,23.9,62.4,0,7,'g21')`, `insert into exposureplan values (51,'p',-1,80,0,0,21,2,1,'e51')`} {
		if err := sched.DB().Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	code, out := get(t, r, http.MethodPost, "/api/v1/planning/filters/draft", `{"targetIds":[20],"filters":["H-a","O-III"]}`)
	payload, _ := out["payload"].(map[string]any)
	if code != http.StatusOK || payload["set_id"] != "filters" || payload["mode"] != "replace" {
		t.Fatalf("%d %v", code, out)
	}
	effects, _ := out["effects"].([]any)
	if len(effects) != 1 {
		t.Fatalf("effects %v", effects)
	}
	if first, _ := effects[0].(map[string]any); first["effect"] != "adds O-III" {
		t.Fatalf("effects %v", effects)
	}
	code, out = get(t, r, http.MethodPost, "/api/v1/planning/filters/draft", `{"targetIds":[20],"filters":["Clear"]}`)
	if code != http.StatusUnprocessableEntity || out["draft"] == nil {
		t.Fatalf("an unknown filter should be 422: %d %v", code, out)
	}
	code, _ = get(t, r, http.MethodPost, "/api/v1/planning/filters/draft", `{"targetIds":[20],"filters":"H-a"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("a bad body should be 400, got %d", code)
	}
}
