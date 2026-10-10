package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebUIFallbackLeavesAPIAlone(t *testing.T) {
	t.Parallel()
	r := gin.New()
	applyWebUI(r, "https://astro-processing.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("api: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/now", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://astro-processing.example/now" {
		t.Fatalf("ui: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/now", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("post: %d", rec.Code)
	}
}

func TestWebUIWithoutURLIsNotFound(t *testing.T) {
	t.Parallel()
	r := gin.New()
	applyWebUI(r, "")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/now", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ui: %d", rec.Code)
	}
}

func TestWebRedirectMapsPages(t *testing.T) {
	t.Parallel()
	base := "https://ap.example"
	cases := map[string]string{
		"/":                              base + "/",
		"/now":                           base + "/now",
		"/tonight/":                      base + "/tonight",
		"/targets":                       base + "/?view=list",
		"/targets?q=m31":                 base + "/?q=m31&view=list",
		"/targets/23":                    base + "/project/23",
		"/targets/23?tab=plans":          base + "/project/23?tab=plans",
		"/targets/23?target=41&tab=goal": base + "/target/41?tab=goal",
		"/mosaics":                       base + "/mosaics",
		"/mosaics/23":                    base + "/mosaics/23",
		"/add?ra=1":                      base + "/add?ra=1",
		"/templates?targets=1,2":         base + "/templates?targets=1%2C2",
		"/history":                       base + "/history",
		"/catalogues":                    base + "/catalogues",
		"/finder":                        base + "/finder",
		"/collabs":                       base + "/collabs",
		"/nope":                          base + "/",
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := webRedirect(base, u); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
