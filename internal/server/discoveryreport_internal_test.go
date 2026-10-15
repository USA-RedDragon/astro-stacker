package server

import (
	"net/http"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveryreport"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func TestDiscoveryReportRoutes(t *testing.T) {
	t.Parallel()
	appStore := memStore(t)
	db := appStore.DB()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoveryAlert{}, &app.DiscoveryFollowup{}, &app.DiscoveryReport{}, &app.Frame{}); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyReportRoutes(r.Group("/api/v1"), nil)
	a := &discoveryAPI{t: t, r: r}
	if code := a.get("/api/v1/discoveries/candidates/1/report", nil); code != http.StatusServiceUnavailable {
		t.Errorf("no service: %d", code)
	}
	r2 := gin.New()
	applyReportRoutes(r2.Group("/api/v1"), &discoveryreport.Service{DB: db})
	a2 := &discoveryAPI{t: t, r: r2}
	if code := a2.get("/api/v1/discoveries/candidates/1/report", nil); code != http.StatusNotFound {
		t.Errorf("missing candidate: %d", code)
	}
	if code := a2.get("/api/v1/discoveries/candidates/x/report", nil); code != http.StatusBadRequest {
		t.Errorf("bad id: %d", code)
	}
	if w := a2.do(http.MethodPost, "/api/v1/discoveries/candidates/1/report/tns-send", nil); w.Code != http.StatusNotFound {
		t.Errorf("send for a missing candidate: %d", w.Code)
	}
}
