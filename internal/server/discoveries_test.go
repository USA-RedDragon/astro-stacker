package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type fakeDiscoveries struct {
	requested []string
	cutouts   map[string][]byte
}

func (f *fakeDiscoveries) Live() discoveries.Live { return discoveries.Live{State: "idle", Queued: 3} }

func (f *fakeDiscoveries) Request(object string, night time.Time, group string) {
	f.requested = append(f.requested, object+"|"+night.Format(time.DateOnly)+"|"+group)
}

func (f *fakeDiscoveries) Cutout(_ context.Context, key string) ([]byte, error) {
	b, ok := f.cutouts[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return b, nil
}

const (
	vetState   = "state"
	objM33     = "M 33"
	nightOct13 = "2026-10-13"
	filterOIII = "O-III"
	reqObject  = "object"
	reqNight   = "night"
	nameIICom  = "II Com"
)

type discoveryAPI struct {
	t   *testing.T
	r   *gin.Engine
	src *fakeDiscoveries
	id  int
	db  *gorm.DB
}

func (a *discoveryAPI) do(method, path string, body map[string]string) *httptest.ResponseRecorder {
	a.t.Helper()
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			a.t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, &b)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	return w
}

func (a *discoveryAPI) get(path string, out any) int {
	a.t.Helper()
	w := a.do(http.MethodGet, path, nil)
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			a.t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	return w.Code
}

func newDiscoveryAPI(t *testing.T) *discoveryAPI {
	t.Helper()
	appStore := memStore(t)
	db := appStore.DB()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoverySource{}, &app.Stack{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]app.Stack{{Object: objM33, Filter: "Luminance"}, {Object: objM33, Filter: "H-a"}}).Error; err != nil {
		t.Fatal(err)
	}
	night := time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC)
	depth := 20.2
	run := app.DiscoveryRun{Object: "IC 3393 Panel 1", Night: night, Group: discoveries.GroupBroadband, Filters: "Luminance,Red", State: app.DiscoveryDone, NightSubs: 16,
		RefSubs: 48, Depth: &depth, Candidates: 2, NewCandidates: 1, Checked: "VSX,SkyBoT", Funnel: `{"brighter":{"raw":3}}`}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	g := 15.9
	cands := []app.DiscoveryCandidate{
		{RunID: run.ID, Object: run.Object, Night: night, Group: run.Group, Kind: app.DiscoveryKindChange, Class: discoveries.ClassKnownVariable, SNR: 50.5, GaiaG: &g, RA: 189.30875, Dec: 14.44222,
			Matches: `[{"catalog":"VSX","name":"` + nameIICom + `","sep_arcsec":0.4}]`, CutoutKey: "discoveries/x/c000/",
			Light: `[{"frame_id":1,"filter":"Luminance","time":"2025-03-01T06:00:00Z","flux":0.1,"flux_err":0.01}]`},
		{RunID: run.ID, Object: run.Object, Night: night, Group: run.Group, Kind: app.DiscoveryKindChange, Class: discoveries.ClassNewVariable, SNR: 8, RA: 189.4, Dec: 14.5, Matches: `[]`},
	}
	if err := db.Create(&cands).Error; err != nil {
		t.Fatal(err)
	}
	src := &fakeDiscoveries{cutouts: map[string][]byte{"discoveries/x/c000/night.png": []byte("png")}}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyDiscoveryRoutes(r.Group("/api/v1"), src)
	return &discoveryAPI{t: t, r: r, src: src, id: cands[0].ID, db: db}
}

func TestDiscoveryCandidateList(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	var list []DiscoveryCandidate
	a.get("/api/v1/discoveries/candidates?night=2025-02-28&class=new_variable_candidate", &list)
	if len(list) != 1 || list[0].SNR != 8 || list[0].Light != nil {
		t.Errorf("class filter %+v", list)
	}
	if code := a.get("/api/v1/discoveries/candidates?class=nope", nil); code != http.StatusBadRequest {
		t.Errorf("unknown class: %d", code)
	}
	a.get("/api/v1/discoveries/candidates?min_snr=10", &list)
	if len(list) != 1 || list[0].Matches[0].Name != nameIICom || list[0].Cutouts == nil {
		t.Errorf("min_snr %+v", list)
	}
}

func TestDiscoveryCandidateDetailAndVetting(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	base := "/api/v1/discoveries/candidates/" + strconv.Itoa(a.id)
	var one DiscoveryCandidate
	a.get(base, &one)
	if len(one.Light) != 1 || one.Run == nil || one.Run.NightSubs != 16 || one.Run.Classes[discoveries.ClassKnownVariable] != 1 || one.Run.Funnel["brighter"].Raw != 3 {
		t.Fatalf("detail %+v", one)
	}
	if w := a.do(http.MethodPut, base+"/vetting", map[string]string{vetState: "maybe"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad vetting state: %d", w.Code)
	}
	w := a.do(http.MethodPut, base+"/vetting", map[string]string{vetState: app.VetKnown, "note": " " + nameIICom + " "})
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one.Vetting != app.VetKnown || one.VetNote != nameIICom || one.VettedAt == nil {
		t.Fatalf("vetting: %s", w.Body)
	}
	var list []DiscoveryCandidate
	a.get("/api/v1/discoveries/candidates?vetting=none", &list)
	if len(list) != 1 || list[0].ID == a.id {
		t.Errorf("vetting filter %+v", list)
	}
	w = a.do(http.MethodGet, base+"/cutout/night.png", nil)
	if w.Code != http.StatusOK || w.Body.String() != "png" || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("cutout: %d %q", w.Code, w.Body)
	}
	if code := a.get(base+"/cutout/other.png", nil); code != http.StatusNotFound {
		t.Errorf("unknown cutout: %d", code)
	}
}

func TestDiscoveryRunsAndRequests(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	if w := a.do(http.MethodPut, "/api/v1/discoveries/candidates/"+strconv.Itoa(a.id)+"/vetting", map[string]string{vetState: app.VetReal}); w.Code != http.StatusOK {
		t.Fatalf("vetting %d", w.Code)
	}
	var runs []DiscoveryRun
	a.get("/api/v1/discoveries/runs?night=2025-02-28", &runs)
	if len(runs) != 1 || runs[0].Vetted != 1 || runs[0].Classes[discoveries.ClassNewVariable] != 1 {
		t.Errorf("runs %+v", runs)
	}
	var nights []map[string]any
	a.get("/api/v1/discoveries/nights", &nights)
	if len(nights) != 1 || nights[0]["night"] != "2025-02-28" || nights[0]["new"] != 1.0 {
		t.Errorf("nights %v", nights)
	}
	if w := a.do(http.MethodPost, "/api/v1/discoveries/runs", map[string]string{reqObject: objM33, reqNight: nightOct13}); w.Code != http.StatusAccepted ||
		len(a.src.requested) != 1 || a.src.requested[0] != objM33+"|"+nightOct13+"|" {
		t.Errorf("request: %d %v", w.Code, a.src.requested)
	}
	if w := a.do(http.MethodPost, "/api/v1/discoveries/runs", map[string]string{reqObject: objM33, reqNight: nightOct13, "group": filterOIII}); w.Code != http.StatusBadRequest {
		t.Errorf("a group the target has no filter of: %d", w.Code)
	}
	if w := a.do(http.MethodPost, "/api/v1/discoveries/runs", map[string]string{reqObject: "Nowhere", reqNight: nightOct13}); w.Code != http.StatusNotFound {
		t.Errorf("an unknown target: %d", w.Code)
	}
	if w := a.do(http.MethodPost, "/api/v1/discoveries/runs", map[string]string{reqObject: objM33, reqNight: nightOct13, "group": "H-a"}); w.Code != http.StatusAccepted {
		t.Errorf("a narrowband group: %d", w.Code)
	}
	var live discoveries.Live
	a.get("/api/v1/discoveries/status", &live)
	if live.Queued != 3 {
		t.Errorf("status %+v", live)
	}
}

func TestDiscoveryCutoutForAGoneClient(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/discoveries/candidates/"+strconv.Itoa(a.id)+"/cutout/night.png", nil)
	w := httptest.NewRecorder()
	a.r.ServeHTTP(w, req)
	if w.Code != statusClientClosed {
		t.Errorf("a request its client gave up on answered %d, want %d", w.Code, statusClientClosed)
	}
}
