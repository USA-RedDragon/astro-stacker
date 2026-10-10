package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/darkneed"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func TestDarkBacklogRoute(t *testing.T) {
	t.Parallel()
	appStore := memStore(t)
	if err := appStore.DB().AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	exp, gain, offset, temp, bin := 300.0, 0.0, 50.0, -20.0, 1.0
	for i, key := range []string{"a", "b"} {
		at := night.Add(time.Duration(i) * time.Hour)
		f := app.Frame{Key: key, Type: lightType, Object: "M31", LastModified: now, Exposure: &exp, Gain: &gain, Offset: &offset,
			SetTemp: &temp, BinX: &bin, DateObs: &at, Night: &night}
		if err := appStore.DB().Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyDarkBacklogRoutes(r.Group("/api/v1"), &darkneed.Publisher{Mode: darkneed.PublishOff}, func() time.Time { return now })
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/coverage/dark-backlog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	combos, ok := got["combos"].([]any)
	if !ok || len(combos) != 1 {
		t.Fatalf("combos %v", got["combos"])
	}
	c, _ := combos[0].(map[string]any)
	if c["combo_key"] != "300|0|50|1|-20" {
		t.Errorf("combo %v", c)
	}
	if c["lights_blocked"] != 2.0 || c["offset"] != 50.0 || c["frames_needed"] != 25.0 || got["frames_needed_basis"] == "" {
		t.Errorf("combo %v", c)
	}
	pub, _ := got["publish"].(map[string]any)
	if pub["mode"] != darkneed.PublishOff {
		t.Errorf("publish %v", got["publish"])
	}
}
