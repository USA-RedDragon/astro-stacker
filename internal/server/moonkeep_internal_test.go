package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/moonkeep"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMoonKeepRoute(t *testing.T) {
	t.Parallel()
	get := func(s *moonkeep.Service, q string) (int, map[string]any) {
		r := gin.New()
		applyMoonKeepRoutes(r.Group("/api/v1"), s)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/moonkeep"+q, nil))
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	if code, _ := get(nil, ""); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	code, body := get(&moonkeep.Service{}, "")
	if code != http.StatusInternalServerError || body["error"] == nil {
		t.Errorf("no site: %d %v", code, body)
	}
	for _, q := range []string{"?start=x&end=2026-01-01T00:00:00Z", "?start=2026-01-01T00:00:00Z", "?start=2026-01-02T00:00:00Z&end=2026-01-01T00:00:00Z",
		"?start=2026-01-01T00:00:00Z&end=2026-01-03T00:00:00Z"} {
		if code, _ := get(&moonkeep.Service{}, q); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&app.Frame{}, &app.StackFrame{}); err != nil {
		t.Fatal(err)
	}
	s := &moonkeep.Service{App: db, Site: func(context.Context) (sky.Site, error) { return sky.Site{Latitude: 31.5, Longitude: -99.4}, nil }}
	code, body = get(s, "?start=2026-01-01T00:00:00Z&end=2026-01-01T01:00:00Z")
	if code != http.StatusOK || body["computed_at"] == nil || body["rules"] == nil {
		t.Fatalf("%d %v", code, body)
	}
	if n, ok := body["night"].([]any); !ok || len(n) != 5 {
		t.Errorf("an hour in 15 minute steps is 5 slots: %v", body["night"])
	}
}
