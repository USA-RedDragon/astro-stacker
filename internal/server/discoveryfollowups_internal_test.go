package server

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func TestDiscoveryFollowupRoutes(t *testing.T) {
	t.Parallel()
	appStore := memStore(t)
	db := appStore.DB()
	if err := db.AutoMigrate(&app.DiscoveryRun{}, &app.DiscoveryCandidate{}, &app.DiscoveryAlert{}, &app.DiscoveryFollowup{}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2025, 2, 28, 0, 0, 0, 0, time.UTC)
	c := app.DiscoveryCandidate{RunID: 1, Object: objM33, Night: night, Group: discoveries.GroupBroadband, Kind: app.DiscoveryKindChange,
		Class: discoveries.ClassSupernova, RA: 23.46, Dec: 30.66, Sign: 1}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyFollowupRoutes(r.Group("/api/v1"))
	a := &discoveryAPI{t: t, r: r}
	path := "/api/v1/discoveries/candidates/" + strconv.Itoa(c.ID) + "/followup"
	var got struct {
		Followup *DiscoveryFollowup `json:"followup"`
	}
	if code := a.get(path, &got); code != http.StatusOK || got.Followup != nil {
		t.Fatalf("no follow-up yet: %d %+v", code, got.Followup)
	}
	seen := night.AddDate(0, 0, 1)
	sep := 0.6
	f := app.DiscoveryFollowup{AlertID: 1, Object: c.Object, Group: c.Group, Night: c.Night, Until: seen, Kind: c.Kind, Class: c.Class,
		RA: c.RA + 1.0/3600, Dec: c.Dec, State: app.FollowupConfirmed, Note: "seen again", FollowNight: &seen, SeenSepArc: &sep}
	if err := db.Create(&f).Error; err != nil {
		t.Fatal(err)
	}
	if code := a.get(path, &got); code != http.StatusOK || got.Followup == nil || got.Followup.State != app.FollowupConfirmed ||
		got.Followup.FollowNight == nil || *got.Followup.FollowNight != "2025-03-01" {
		t.Fatalf("follow-up: %d %+v", code, got.Followup)
	}
	var list []DiscoveryFollowup
	if code := a.get("/api/v1/discoveries/followups?night=2025-02-28", &list); code != http.StatusOK || len(list) != 1 {
		t.Errorf("list: %d %+v", code, list)
	}
	if code := a.get("/api/v1/discoveries/followups?night=2025-03-01", &list); code != http.StatusOK || len(list) != 0 {
		t.Errorf("other night: %d %+v", code, list)
	}
	if code := a.get("/api/v1/discoveries/followups?night=nope", nil); code != http.StatusBadRequest {
		t.Errorf("bad night: %d", code)
	}
}
