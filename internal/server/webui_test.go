package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebUIFallbackLeavesAPIAlone(t *testing.T) {
	t.Parallel()
	r := gin.New()
	applyWebUI(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("api: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/now", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("ui: %d", rec.Code)
	}
}
