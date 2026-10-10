package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/catalog"
	"github.com/USA-RedDragon/astro-stacker/internal/discover"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func discoverRouter(t *testing.T, d *discover.Service) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyDiscoverRoutes(r.Group("/api/v1"), Extras{Discover: d})
	return r
}

func discoverGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w
}

func TestDiscoverRoutesOff(t *testing.T) {
	t.Parallel()
	r := discoverRouter(t, nil)
	for _, p := range []string{"/api/v1/catalog/search?q=M31", "/api/v1/catalogues", "/api/v1/finder", "/api/v1/collabs"} {
		if w := discoverGet(t, r, p); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestDiscoverRoutes(t *testing.T) {
	t.Parallel()
	ix, err := catalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.Stack{}, &app.Frame{}, &app.ObjectXref{}); err != nil {
		t.Fatal(err)
	}
	d := &discover.Service{Catalog: ix, AppDB: db, Rig: discover.Rig{Frame: sky.Frame{FocalLength: 405, PixelSize: 3.76, WidthPx: 6248, HeightPx: 4176}}}
	r := discoverRouter(t, d)
	w := discoverGet(t, r, "/api/v1/catalog/search?q=Garlic%20Nebula&limit=3")
	var matches []catalog.Match
	if err := json.Unmarshal(w.Body.Bytes(), &matches); err != nil || w.Code != http.StatusOK || len(matches) == 0 || matches[0].Object.Designation != "G116.9+00.2" {
		t.Fatalf("search: %d %s", w.Code, w.Body.String())
	}
	if w := discoverGet(t, r, "/api/v1/catalog/search?q="); w.Code != http.StatusOK || w.Body.String() != "[]" {
		t.Errorf("empty search: %d %s", w.Code, w.Body.String())
	}
	if w := discoverGet(t, r, "/api/v1/catalog/cone?ra=359.8&dec=62.44&radius=0.3"); w.Code != http.StatusOK {
		t.Errorf("cone: %d", w.Code)
	}
	if w := discoverGet(t, r, "/api/v1/catalog/cone?ra=1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad cone: %d", w.Code)
	}
	if w := discoverGet(t, r, "/api/v1/catalog/objects/M31"); w.Code != http.StatusOK {
		t.Errorf("object: %d %s", w.Code, w.Body.String())
	}
	if w := discoverGet(t, r, "/api/v1/catalog/objects/nope"); w.Code != http.StatusNotFound {
		t.Errorf("missing object: %d", w.Code)
	}
	if w := discoverGet(t, r, "/api/v1/catalog/resolve"); w.Code != http.StatusBadRequest {
		t.Errorf("empty resolve: %d", w.Code)
	}
	if w := discoverGet(t, r, "/api/v1/catalog/resolve?name=Abell%2085&ra=359.8&dec=62.44"); w.Code != http.StatusOK {
		t.Errorf("resolve: %d", w.Code)
	}
	if w := discoverGet(t, r, "/api/v1/catalogues/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown list: %d", w.Code)
	}
	for _, p := range []string{"/api/v1/catalogues", "/api/v1/catalogues/messier", "/api/v1/catalog/matches?decided=1", "/api/v1/catalog/subjects",
		"/api/v1/catalog/subjects/links?subject=x", "/api/v1/catalog/sources", "/api/v1/finder?fit=one&types=em&months=10,11&minFill=0.3&sort=fill", "/api/v1/collabs"} {
		if w := discoverGet(t, r, p); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
}
