package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/collabswitch"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const switchUser = "ann"

func collabSwitchRouter(t *testing.T, sw *collabswitch.Service) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyCollabSwitchRoutes(r.Group("/api/v1"), sw)
	return r
}

func collabSwitchDo(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webauth-User", switchUser)
	r.ServeHTTP(w, req)
	return w
}

func TestCollabSwitchRoutes(t *testing.T) {
	t.Parallel()
	off := collabSwitchRouter(t, nil)
	if w := collabSwitchDo(t, off, http.MethodGet, "/api/v1/collab-switch", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Errorf("off GET %d %s", w.Code, w.Body)
	}
	if w := collabSwitchDo(t, off, http.MethodPost, "/api/v1/collab-switch", `{"on":true}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("off POST %d", w.Code)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "app.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&schedcmd.Record{}, &app.CollabSwitch{}); err != nil {
		t.Fatal(err)
	}
	cmds := &schedcmd.Service{Log: schedcmd.NewGormLog(db), AppDB: db}
	r := collabSwitchRouter(t, &collabswitch.Service{NINA: collabswitch.NewNINA(""), DB: db, Commands: cmds, Log: cmds.Log})
	if w := collabSwitchDo(t, r, http.MethodPost, "/api/v1/collab-switch", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("POST without on %d", w.Code)
	}
	w := collabSwitchDo(t, r, http.MethodPost, "/api/v1/collab-switch", `{"on":true}`)
	var out struct {
		Status collabswitch.Status `json:"status"`
		Record schedcmd.Record     `json:"record"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("POST %d %s %v", w.Code, w.Body, err)
	}
	if out.Status.Desired == nil || !out.Status.Desired.On || out.Status.Desired.SetBy != switchUser || out.Record.Author != switchUser || out.Status.Configured {
		t.Errorf("POST %+v", out)
	}
	if w := collabSwitchDo(t, r, http.MethodPost, "/api/v1/collab-switch/read", ""); w.Code != http.StatusOK {
		t.Errorf("read %d %s", w.Code, w.Body)
	}
}
