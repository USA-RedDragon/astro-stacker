package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/schedauto"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/schedglobal"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestSchedulingRoutes(t *testing.T) {
	t.Parallel()
	if code, _ := do(t, newHandler(t, memDB(t, "s0"), memDB(t, "a0"), server.Extras{}), http.MethodGet, "/api/v1/scheduling", ""); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	appDB := memDB(t, "a")
	if err := appDB.AutoMigrate(&schedcmd.Record{}, &app.GlobalSchedulingSetting{}, &app.SchedulingOriginal{}, &app.Frame{}); err != nil {
		t.Fatal(err)
	}
	cmds := &schedcmd.Service{Log: schedcmd.NewGormLog(appDB), AppDB: appDB}
	s := &schedauto.Service{App: appDB, Site: func(context.Context) (sky.Site, error) { return sky.Site{Latitude: 31.5, Longitude: -99.4}, nil }}
	h := newHandler(t, memDB(t, "s"), appDB, server.Extras{Commands: cmds, Scheduling: s})
	code, body := do(t, h, http.MethodGet, "/api/v1/scheduling", "")
	var res schedauto.Result
	if code != http.StatusOK || json.Unmarshal(body, &res) != nil || res.Saved || res.Global != schedglobal.Defaults() || res.Mode != schedauto.ModeOff {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := do(t, h, http.MethodPost, "/api/v1/scheduling/global", `{"settings":{"ditherEvery":-1}}`); code != http.StatusBadRequest {
		t.Errorf("bad value: %d %s", code, body)
	}
	if code, body := do(t, h, http.MethodPost, "/api/v1/scheduling/global", `{"settings":{"ditherEvery":3,"filterSwitchFrequency":1,"flatsHandling":1,"enableGrader":true}}`); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	saved, err := schedglobal.Load(context.Background(), appDB)
	if err != nil || saved.Settings == nil || saved.Settings.DitherEvery != 3 {
		t.Fatalf("saved %+v %v", saved, err)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/scheduling", ""); code != http.StatusOK || json.Unmarshal(body, &res) != nil || !res.Saved || res.Global.DitherEvery != 3 {
		t.Errorf("after save: %d %s", code, body)
	}
	if code, body := do(t, h, http.MethodGet, "/api/v1/scheduling/originals", ""); code != http.StatusOK || string(body) != "[]" {
		t.Errorf("originals: %d %s", code, body)
	}
	if code, _ := do(t, h, http.MethodGet, "/api/v1/scheduling/projection", ""); code != http.StatusServiceUnavailable {
		t.Errorf("no observatory: %d", code)
	}
}
