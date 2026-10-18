package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/binaries"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/check"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting/period"
	"github.com/gin-gonic/gin"
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
	db := memStore(t).DB()
	if err := db.AutoMigrate(&app.DiscoverySource{}, &app.DiscoveryVet{}, &app.BinaryBook{}, &app.BinaryFacts{}, &app.BinarySetting{},
		&app.EclipseRequest{}, &app.ShareImage{}); err != nil {
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
	rep := vetting.Report{SourceID: src.ID, Star: "Gaia DR3 42 · Gaia G 15.00", Suggestion: vetting.Suggestion{Confidence: "high"},
		Ephemeris: &period.Ephemeris{Period: 5.4, PeriodErr: 1e-5, T0: 2459000, T0Err: 0.001, Kind: period.KindEclipsing,
			Eclipses: []period.Eclipse{{Phase: 0, Width: 0.02, Shape: 2, Depth: map[string]float64{"ZTF r": 0.3}}}},
		Groups: []vetting.Group{{ID: check.GroupSurveys, Checks: []check.Check{check.New(check.Half, check.Pass, "normal"),
			check.New(check.Double, check.NotRun, period.SameDepthOddEven+", so two equal dips fit.")}}}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&app.DiscoveryVet{SourceID: src.ID, State: "done", Report: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	applyBinaryRoutes(r.Group("/api/v1"), &binaries.Service{DB: db})
	return r, src, other
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
