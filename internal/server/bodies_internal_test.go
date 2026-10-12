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
