package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/sublength"
	"github.com/gin-gonic/gin"
)

func TestSubLengthRoute(t *testing.T) {
	t.Parallel()
	get := func(s *sublength.Service) (int, map[string]any) {
		r := gin.New()
		applySubLengthRoutes(r.Group("/api/v1"), s)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/sublength?object=M31", nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	code, body := get(&sublength.Service{})
	if code != http.StatusOK || body["reason"] != "no scheduler database" || body["mode"] != "off" {
		t.Errorf("no databases: %d %v", code, body)
	}
	if rows, ok := body["rows"].([]any); !ok || len(rows) != 0 {
		t.Errorf("rows %v, want an empty list", body["rows"])
	}
}
