package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func call(t *testing.T, r *gin.Engine, method, path string, body any, out any) int {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, &b)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %s", path, w.Body)
		}
	}
	return w.Code
}

// binaryServer serves the binary routes over a made-up confirmed star and
// an unconfirmed source.
func binaryServer(t *testing.T) (*gin.Engine, app.DiscoverySource, app.DiscoverySource) {
	t.Helper()
	r, _, src, other := binaryServerDB(t)
	return r, src, other
}

// binaryServerDB is binaryServer with its database.
func binaryServerDB(t *testing.T) (*gin.Engine, *gorm.DB, app.DiscoverySource, app.DiscoverySource) {
	t.Helper()
	db := memStore(t).DB()
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.VetBuild{}, &app.BinaryBook{}, &app.BinaryFacts{}, &app.BinarySetting{}, &app.DipWatch{},
		&app.EclipseRequest{}, &app.ShareImage{}, &app.EclipseTiming{}, &app.DipperEphemeris{}, &app.DipResult{}); err != nil {
		t.Fatal(err)
	}
	src := app.DiscoverySource{Object: "Made-up field", Kind: app.DiscoveryKindChange, RA: 10, Dec: 20, Night: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Vetting: app.VetReal}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	other := app.DiscoverySource{Object: "Made-up field", Kind: app.DiscoveryKindChange, RA: 11, Dec: 21, Night: src.Night}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	rep := vetting.Report{SourceID: src.ID, Star: "Gaia DR3 42 · Gaia G 15.00", Suggestion: vetting.Suggestion{State: app.VetReal, Confidence: "high", Kind: vetting.KindEclipsingBinary},
		Ephemeris: &period.Ephemeris{Period: 5.4, PeriodErr: 1e-5, T0: 2459000, T0Err: 0.001, Kind: period.KindEclipsing,
			Eclipses: []period.Eclipse{{Phase: 0, Width: 0.02, Shape: 2, Depth: map[string]float64{"ZTF r": 0.3}}}},
		Groups: []vetting.Group{{ID: check.GroupSurveys, Checks: []check.Check{check.New(check.Half, check.Pass, "normal"),
			check.New(check.Echo, check.Pass, "stands out"), check.New(check.Double, check.NotRun, period.SameDepthOddEven+", so two equal dips fit.")}}}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DiscoveryVet{SourceID: src.ID, State: "done", Report: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	applyBinaryRoutes(r.Group("/api/v1"), &binaries.Service{DB: db})
	return r, db, src, other
}

func TestBinaryListAndUpdate(t *testing.T) {
	t.Parallel()
	r, src, _ := binaryServer(t)
	var list BinaryList
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/binaries", nil, &list); code != http.StatusOK || len(list.Stars) != 1 {
		t.Fatalf("list: %d %+v", code, list)
	}
	s := list.Stars[0]
	if s.Name != "Gaia DR3 42" || s.PeriodText != "5.40000" || len(s.Flags) != 1 || s.Flags[0].ID != binaries.FlagTwice || s.VSX.Submitted {
		t.Errorf("star %+v", s)
	}
	path := "/api/v1/discoveries/binaries/" + strconv.Itoa(src.ID)
	var got binaries.Binary
	if code := call(t, r, http.MethodPut, path, map[string]any{"vsx_submitted": true, "vsx_ref": "https://example.org/vsx/1", "notes": "line one\nline two"}, &got); code != http.StatusOK ||
		!got.VSX.Submitted || got.VSX.SubmittedAt == nil || got.Notes != "line one\nline two" {
		t.Errorf("update: %d %+v", code, got)
	}
}

func TestBinaryKitRoutes(t *testing.T) {
	t.Parallel()
	r, src, other := binaryServer(t)
	if code := call(t, r, http.MethodPut, "/api/v1/discoveries/binaries/settings", map[string]string{"discoverer": " A. Observer "}, nil); code != http.StatusOK {
		t.Errorf("settings: %d", code)
	}
	path := "/api/v1/discoveries/binaries/" + strconv.Itoa(src.ID)
	var kit binaries.Kit
	if code := call(t, r, http.MethodGet, path+"/kit", nil, &kit); code != http.StatusOK || len(kit.Fields) == 0 {
		t.Fatalf("kit: %d", code)
	}
	for _, f := range kit.Fields {
		if f.Key == "discoverer" && f.Value != "A. Observer" {
			t.Errorf("discoverer %+v", f)
		}
	}
	if code := call(t, r, http.MethodPut, path, map[string]any{"checklist": map[string]bool{"check_name": true, "vsx_clear": true}}, nil); code != http.StatusOK {
		t.Errorf("checklist: %d", code)
	}
	if code := call(t, r, http.MethodGet, path+"/kit", nil, &kit); code != http.StatusOK {
		t.Fatalf("kit: %d", code)
	}
	for _, c := range kit.Checklist {
		if (c.Key == "check_name") != c.Done {
			t.Errorf("checklist item %+v", c)
		}
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/binaries/"+strconv.Itoa(other.ID)+"/kit", nil, nil); code != http.StatusNotFound {
		t.Errorf("an unconfirmed source's kit: %d", code)
	}
	if code := call(t, r, http.MethodGet, path+"/files/phased.png", nil, nil); code != http.StatusNotFound {
		t.Errorf("a file not made yet: %d", code)
	}
}

func TestEclipseAndShareRoutes(t *testing.T) {
	t.Parallel()
	r, _, other := binaryServer(t)
	var plan binaries.EclipsePlan
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/eclipses?all=1", nil, &plan); code != http.StatusOK || plan.SiteKnown {
		t.Errorf("plan: %d %+v", code, plan)
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/eclipses?days=900", nil, nil); code != http.StatusBadRequest {
		t.Errorf("too many days: %d", code)
	}
	var sh binaries.Share
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/sources/"+strconv.Itoa(other.ID)+"/share", nil, &sh); code != http.StatusOK || sh.State != binaries.SharePending {
		t.Errorf("share asked for: %d %+v", code, sh)
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/sources/999/share", nil, nil); code != http.StatusNotFound {
		t.Errorf("no such source: %d", code)
	}
}

func TestBinaryTimingsRoute(t *testing.T) {
	t.Parallel()
	r, db, src, other := binaryServerDB(t)
	path := "/api/v1/discoveries/binaries/" + strconv.Itoa(src.ID) + "/timings"
	var rec binaries.TimingRecord
	if code := call(t, r, http.MethodGet, path, nil, &rec); code != http.StatusOK || !strings.HasPrefix(rec.Summary, "Not ready yet") {
		t.Fatalf("before the facts: %d %+v", code, rec)
	}
	e := &period.Ephemeris{Period: 5.4, PeriodErr: 1e-5, T0: 2459000, T0Err: 0.001, Kind: period.KindEclipsing,
		Eclipses: []period.Eclipse{{Phase: 0, Width: 0.02, Shape: 2, Depth: map[string]float64{"ZTF r": 0.3}}}}
	raw, err := json.Marshal(binaries.Facts{Ephemeris: e, Gaia: &binaries.GaiaFacts{RA: 10, Dec: 20, G: 15}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.BinaryFacts{SourceID: src.ID, Version: binaries.FactsVersion, State: binaries.FactsReady, Facts: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	bjd := e.T0 + 1000*e.Period + 2.0/1440
	if err := db.Create(&app.EclipseTiming{SourceID: src.ID, Kind: binaries.KindPrimary, Predicted: time.Date(2023, 9, 1, 0, 0, 0, 0, time.UTC), BJD: bjd, Err: 0.4 / 1440,
		State: app.TimingGood, Method: "model", Filter: "Red", Points: 80, Night: "2023-08-31"}).Error; err != nil {
		t.Fatal(err)
	}
	if code := call(t, r, http.MethodGet, path, nil, &rec); code != http.StatusOK || len(rec.Timings) != 1 || len(rec.Trends) != 4 {
		t.Fatalf("record: %d %+v", code, rec)
	}
	if v := rec.Timings[0]; v.LateText != "2.0 minutes late" || v.Cycle != 1000 {
		t.Errorf("timing %+v", v)
	}
	for _, tr := range rec.Trends {
		if tr.State != "need" {
			t.Errorf("one timing tested a trend: %+v", tr)
		}
	}
	var list BinaryList
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/binaries", nil, &list); code != http.StatusOK || list.Stars[0].Timing.Count != 1 {
		t.Errorf("card timing: %d %+v", code, list.Stars[0].Timing)
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/binaries/"+strconv.Itoa(other.ID)+"/timings", nil, nil); code != http.StatusNotFound {
		t.Errorf("an unconfirmed source's timings: %d", code)
	}
}

func TestDipWatchLinkAndLiveCurve(t *testing.T) {
	t.Parallel()
	r, src, _ := binaryServer(t)
	var live LiveCurveView
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/live-curve", nil, &live); code != http.StatusOK || live.Live != nil {
		t.Errorf("live curve with no run: %d %+v", code, live)
	}
	start := time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)
	body := map[string]any{"source_id": src.ID, "project_guid": "p-1", "start": start, "end": start.Add(2 * time.Hour),
		"depth_low": 0.15, "depth_high": 0.6, "active_share": 0.25}
	var w app.DipWatch
	if code := call(t, r, http.MethodPut, "/api/v1/discoveries/dip-watches", body, &w); code != http.StatusOK || w.ID == 0 || w.SourceID != src.ID {
		t.Fatalf("save: %d %+v", code, w)
	}
	body["depth_high"] = 0.7
	if code := call(t, r, http.MethodPut, "/api/v1/discoveries/dip-watches", body, &w); code != http.StatusOK || w.DepthHigh != 0.7 {
		t.Errorf("save again: %d %+v", code, w)
	}
	for name, bad := range map[string]map[string]any{
		"window": {"source_id": src.ID, "project_guid": "p-2", "start": start, "end": start},
		"depths": {"source_id": src.ID, "project_guid": "p-2", "start": start, "end": start.Add(time.Hour), "depth_low": 0.5, "depth_high": 0.1},
		"source": {"source_id": 999999, "project_guid": "p-2", "start": start, "end": start.Add(time.Hour)},
	} {
		if code := call(t, r, http.MethodPut, "/api/v1/discoveries/dip-watches", bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	var list struct {
		DipWatches []app.DipWatch `json:"dip_watches"`
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/dip-watches", nil, &list); code != http.StatusOK || len(list.DipWatches) != 1 {
		t.Errorf("list: %d %+v", code, list)
	}
}

func TestDipperRoutes(t *testing.T) {
	t.Parallel()
	r, db, eb, _ := binaryServerDB(t)
	dip := app.DiscoverySource{Object: eb.Object, Kind: app.DiscoveryKindChange, RA: 12, Dec: 22, Night: eb.Night, Vetting: app.VetReal}
	if err := db.Create(&dip).Error; err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/discoveries/dippers/" + strconv.Itoa(dip.ID)
	var v binaries.DipperView
	if code := call(t, r, http.MethodGet, base, nil, &v); code != http.StatusOK || v.Ephemeris != nil || len(v.Windows) != 0 {
		t.Fatalf("before a cycle: %d %+v", code, v)
	}
	if code := call(t, r, http.MethodPut, base+"/every-window", map[string]bool{"on": true}, nil); code != http.StatusNotFound {
		t.Errorf("every window with no cycle: %d", code)
	}
	cycle := binaries.DipCycle{Period: 9.3564, EpochMJD: 61297.08, WindowHours: 20, DepthLow: 0.15, DepthHigh: 0.6, DurLowHours: 2, DurHighHours: 18, ActiveShare: 0.25}
	bad := cycle
	bad.WindowHours = 300
	if code := call(t, r, http.MethodPut, base+"/ephemeris", bad, nil); code != http.StatusBadRequest {
		t.Errorf("window longer than the period: %d", code)
	}
	if code := call(t, r, http.MethodPut, base+"/ephemeris", cycle, &v); code != http.StatusOK || v.Ephemeris == nil || v.WindowsText != "every 9.356 days, 20 hours wide" || len(v.Windows) == 0 {
		t.Fatalf("set cycle: %d %+v", code, v)
	}
	if code := call(t, r, http.MethodPut, base+"/every-window", map[string]bool{"on": true}, &v); code != http.StatusOK || !v.EveryWindow {
		t.Errorf("every window: %d %+v", code, v)
	}
	if code := call(t, r, http.MethodPut, base+"/ephemeris", cycle, &v); code != http.StatusOK || !v.EveryWindow {
		t.Errorf("a new cycle turned every window off: %d %+v", code, v)
	}
	var list BinaryList
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/binaries", nil, &list); code != http.StatusOK {
		t.Fatal(code)
	}
	found := false
	for _, o := range list.Others {
		if o.SourceID == dip.ID {
			found = o.Dipper != nil && o.Dipper.DipsText == "0.15 to 0.6 mag, 2 to 18 hours, in about 1 window in 4" && o.Dipper.EveryWindow
		}
	}
	if !found {
		t.Errorf("the dipper's card has no dip cycle: %+v", list.Others)
	}
	if code := call(t, r, http.MethodGet, "/api/v1/discoveries/dippers/"+strconv.Itoa(eb.ID), nil, nil); code != http.StatusNotFound {
		t.Errorf("an eclipsing binary as a dipper: %d", code)
	}
	if code := call(t, r, http.MethodPost, base+"/requests", map[string]time.Time{"mid": time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}, nil); code != http.StatusBadRequest {
		t.Errorf("a request between windows: %d", code)
	}
}
