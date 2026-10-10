package webui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/USA-RedDragon/astro-stacker/internal/webui"
)

const (
	indexHTML = "<html>app</html>"
	noCache   = "no-cache"
)

func TestHandler(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":      {Data: []byte(indexHTML)},
		"assets/app-1.js": {Data: []byte("js")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	}
	h := webui.Handler(files)
	cases := []struct {
		path   string
		status int
		body   string
		cache  string
	}{
		{"/", 200, indexHTML, noCache},
		{"/now", 200, indexHTML, noCache},
		{"/targets/12", 200, indexHTML, noCache},
		{"/assets/app-1.js", 200, "js", "public, max-age=31536000, immutable"},
		{"/assets/missing.js", 404, "", ""},
		{"/missing.png", 404, "", ""},
		{"/favicon.svg", 200, "<svg/>", ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, c.path, nil))
		if rec.Code != c.status {
			t.Fatalf("%s: status %d", c.path, rec.Code)
		}
		if c.body != "" && rec.Body.String() != c.body {
			t.Fatalf("%s: body %q", c.path, rec.Body.String())
		}
		if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
			t.Fatalf("%s: cache %q", c.path, rec.Header().Get("Cache-Control"))
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", rec.Code)
	}
}

func TestEmbeddedBuildOrPlaceholder(t *testing.T) {
	t.Parallel()
	files := webui.FS()
	rec := httptest.NewRecorder()
	webui.Handler(files).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if webui.Built(files) != (rec.Code == http.StatusOK) {
		t.Fatalf("built=%v status=%d", webui.Built(files), rec.Code)
	}
}
