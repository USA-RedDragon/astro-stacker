package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/coverage"
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

func TestDarkBacklogCountsAndListsEveryRejectedDark(t *testing.T) {
	t.Parallel()
	appStore := memStore(t)
	if err := appStore.DB().AutoMigrate(&app.Frame{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	night := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	exp, gain, offset, temp, bin, leak := 300.0, 0.0, 50.0, -20.0, 1.0, 12.5
	const total = 55
	for i := range total {
		at := night.Add(time.Duration(i) * time.Minute)
		f := app.Frame{Key: fmt.Sprintf("dark-%02d", i), Type: "DARK", LastModified: now, Exposure: &exp, Gain: &gain, Offset: &offset,
			SetTemp: &temp, BinX: &bin, DateObs: &at, Night: &night, LightLeak: &leak}
		if err := appStore.DB().Create(&f).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyDarkBacklogRoutes(r.Group("/api/v1"), &darkneed.Publisher{Mode: darkneed.PublishOff}, func() time.Time { return now })
	get := func(path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var b struct {
		Rejected      []map[string]any `json:"rejected"`
		RejectedTotal int              `json:"rejected_total"`
	}
	get("/api/v1/coverage/dark-backlog", &b)
	if len(b.Rejected) != coverage.RejectedListed || b.RejectedTotal != total {
		t.Fatalf("listed %d of %d, want %d of %d", len(b.Rejected), b.RejectedTotal, coverage.RejectedListed, total)
	}
	var all []map[string]any
	get("/api/v1/coverage/dark-backlog/rejected", &all)
	if len(all) != total || all[0]["key"] != "dark-54" || all[total-1]["key"] != "dark-00" {
		t.Fatalf("full list has %d, first %v, last %v", len(all), all[0]["key"], all[len(all)-1]["key"])
	}
}
