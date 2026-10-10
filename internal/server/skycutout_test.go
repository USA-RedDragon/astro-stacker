package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/skycutout"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestSkyCutoutRoute(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xDB, 9})
	}))
	defer upstream.Close()
	cache := memDB(t, "cutouts")
	if err := cache.AutoMigrate(&app.SkyCutout{}); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{Cutouts: &skycutout.Service{DB: cache, Endpoint: upstream.URL, PerMinute: 1}})
	const path = "/api/v1/sky/cutout?ra=83.8&dec=-5.4&fov=1&rotation=10&width=200&height=100"
	get := func(p, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, p, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := get(path, "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != skycutout.ContentType || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") == "" {
		t.Fatalf("cutout %d %v", w.Code, w.Header())
	}
	if again := get(path, w.Header().Get("ETag")); again.Code != http.StatusNotModified {
		t.Errorf("conditional %d", again.Code)
	}
	if code, _ := do(t, h, http.MethodGet, "/api/v1/sky/cutout?ra=83.8&dec=-95&fov=1", ""); code != http.StatusBadRequest {
		t.Errorf("bad dec %d", code)
	}
	if limited := get("/api/v1/sky/cutout?ra=10&dec=10&fov=1", ""); limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Errorf("limit %d %v", limited.Code, limited.Header())
	}
	off := newHandler(t, memDB(t, "s2"), memDB(t, "a2"), server.Extras{})
	if code, _ := do(t, off, http.MethodGet, "/api/v1/sky/cutout?ra=10&dec=10&fov=1", ""); code != http.StatusServiceUnavailable {
		t.Errorf("off %d", code)
	}
}
