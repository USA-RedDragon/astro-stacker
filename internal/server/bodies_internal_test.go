package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/gin-gonic/gin"
)

func TestBodiesRoutes(t *testing.T) {
	t.Parallel()
	get := func(s *bodies.Service, path string) (int, map[string]any) {
		r := gin.New()
		applyBodiesRoutes(r.Group("/api/v1"), s)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil, "/api/v1/bodies"); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	s := &bodies.Service{}
	if code, body := get(s, "/api/v1/bodies"); code != http.StatusServiceUnavailable || body[errorKey] != bodiesNotBuilt {
		t.Errorf("before the first build: %d %v", code, body)
	}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, body := get(s, "/api/v1/bodies")
	if code != http.StatusOK || body["skipped"] != bodies.SkipNoScheduler || body["mode"] != bodies.ModeOn {
		t.Errorf("no scheduler: %d %v", code, body)
	}
	if tabs, ok := body["tables"].([]any); !ok || len(tabs) != 0 {
		t.Errorf("tables %v", body["tables"])
	}
	code, body = get(s, "/api/v1/bodies/positions?target_guid=x")
	if rows, ok := body["rows"].([]any); code != http.StatusOK || !ok || len(rows) != 0 {
		t.Errorf("positions: %d %v", code, body)
	}
}

func TestBodyNightRoutes(t *testing.T) {
	t.Parallel()
	get := func(s *bodies.NightService, path string) (int, map[string]any) {
		r := gin.New()
		applyBodyNightRoutes(r.Group("/api/v1"), s)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil, "/api/v1/bodies/nights"); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	s := &bodies.NightService{}
	if code, body := get(s, "/api/v1/bodies/nights?target_guid=x"); code != http.StatusServiceUnavailable || body[errorKey] == nil {
		t.Errorf("before the first build: %d %v", code, body)
	}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, body := get(s, "/api/v1/bodies/nights")
	if code != http.StatusOK || body["skipped"] != bodies.SkipNoScheduler || body["basis"] != bodies.NightBasis {
		t.Errorf("status: %d %v", code, body)
	}
	if code, body := get(s, "/api/v1/bodies/nights?target_guid=x"); code != http.StatusNotFound || body[errorKey] == nil {
		t.Errorf("unknown target: %d %v", code, body)
	}
}

func TestCometRoute(t *testing.T) {
	t.Parallel()
	get := func(l *bodies.CometList, path string) (int, map[string]any) {
		r := gin.New()
		applyCometRoutes(r.Group("/api/v1"), l)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil, "/api/v1/comets"); code != http.StatusServiceUnavailable {
		t.Errorf("no list: %d", code)
	}
	for _, q := range []string{"max_mag=bright", "max_mag=NaN", "max_mag=Inf", "min_alt=NaN", "min_alt=91"} {
		if code, _ := get(&bodies.CometList{}, "/api/v1/comets?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code, body := get(&bodies.CometList{}, "/api/v1/comets"); code != http.StatusServiceUnavailable || body[errorKey] == nil {
		t.Errorf("no site: %d %v", code, body)
	}
}

func TestAsteroidRoute(t *testing.T) {
	t.Parallel()
	get := func(l *bodies.AsteroidList, path string) (int, map[string]any) {
		r := gin.New()
		applyAsteroidRoutes(r.Group("/api/v1"), l)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil, "/api/v1/asteroids"); code != http.StatusServiceUnavailable {
		t.Errorf("no list: %d", code)
	}
	for _, q := range []string{"max_mag=bright", "max_mag=NaN", "min_alt=-91", "min_alt=x"} {
		if code, _ := get(&bodies.AsteroidList{Comets: &bodies.CometList{}}, "/api/v1/asteroids?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code, body := get(&bodies.AsteroidList{Comets: &bodies.CometList{}}, "/api/v1/asteroids?max_mag=12&min_alt=30"); code != http.StatusServiceUnavailable || body[errorKey] == nil {
		t.Errorf("no site: %d %v", code, body)
	}
}
