package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
	"github.com/USA-RedDragon/astro-stacker/internal/discoverywatch"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func TestDiscoverySourceInbox(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	var page DiscoverySpotList
	if code := a.get("/api/v1/discoveries/sources", &page); code != http.StatusOK || page.Total != 0 || page.Since == nil {
		t.Fatalf("default 30 days hides the 2025 night: %d %+v", code, page)
	}
	a.get("/api/v1/discoveries/sources?since=all", &page)
	if page.Total != 2 || page.Counts.Look != 1 || page.Counts.Known != 1 || page.Since != nil {
		t.Fatalf("all nights %+v", page)
	}
	if !page.Sources[0].Look || page.Sources[0].Class != discoveries.ClassNewVariable || page.Sources[0].Seen != 1 || page.Sources[0].Compared != 1 {
		t.Errorf("needs a look first: %+v", page.Sources[0])
	}
	known := page.Sources[1]
	if known.Best.ID != a.id || len(known.Nights) != 1 || known.Nights[0].Night != "2025-02-28" || known.Best.Cutouts == nil || known.Best.SourceID == nil {
		t.Errorf("known source %+v", known)
	}
	a.get("/api/v1/discoveries/sources?night=2025-02-28&view=known", &page)
	if len(page.Sources) != 1 || page.Total != 1 || page.Counts.All != 2 {
		t.Errorf("night deep link %+v", page)
	}
	for _, bad := range []string{"view=nope", "night=x", "since=x", "until=x"} {
		if code := a.get("/api/v1/discoveries/sources?"+bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestDiscoverySourceVettingAndRuns(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	var page DiscoverySpotList
	a.get("/api/v1/discoveries/sources?since=all&view=known", &page)
	known := page.Sources[0]
	base := "/api/v1/discoveries/sources/" + strconv.Itoa(known.ID)
	w := a.do(http.MethodPut, base+"/vetting", map[string]string{vetState: app.VetKnown, "note": " " + nameIICom + " "})
	var one DiscoverySpot
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one.Vetting != app.VetKnown || one.VetNote != nameIICom || one.Nights[0].Candidates[0].Vetting != app.VetKnown {
		t.Fatalf("source vetting %d %s", w.Code, w.Body)
	}
	if code := a.get("/api/v1/discoveries/sources/999", nil); code != http.StatusNotFound {
		t.Errorf("unknown source %d", code)
	}
	if w := a.do(http.MethodPut, base+"/vetting", map[string]string{vetState: "maybe"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad state %d", w.Code)
	}

	var runs []DiscoveryRun
	a.get("/api/v1/discoveries/runs?since=2025-02-01&limit=5", &runs)
	if len(runs) != 1 || runs[0].Look != 1 || runs[0].Vetted != 1 {
		t.Errorf("recent runs %+v", runs)
	}
	if code := a.get("/api/v1/discoveries/runs?since=soon", nil); code != http.StatusBadRequest {
		t.Errorf("bad since %d", code)
	}
}

func TestCandidateVettingVetsItsSource(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	db := a.db
	night := time.Date(2025, 3, 2, 0, 0, 0, 0, time.UTC)
	var first app.DiscoveryCandidate
	db.First(&first, a.id)
	again := first
	again.ID, again.Night, again.Vetting = 0, night, ""
	again.RA += 0.5 / 3600
	if err := db.Create(&again).Error; err != nil {
		t.Fatal(err)
	}
	if w := a.do(http.MethodPut, "/api/v1/discoveries/candidates/"+strconv.Itoa(a.id)+"/vetting", map[string]string{vetState: app.VetReal}); w.Code != http.StatusOK {
		t.Fatalf("vetting %d %s", w.Code, w.Body)
	}
	var c DiscoveryCandidate
	a.get("/api/v1/discoveries/candidates/"+strconv.Itoa(again.ID), &c)
	if c.Vetting != app.VetReal || c.SourceID == nil {
		t.Errorf("the other night of the spot: %q %v", c.Vetting, c.SourceID)
	}
	var page DiscoverySpotList
	a.get("/api/v1/discoveries/sources?since=all&view=vetted", &page)
	if len(page.Sources) != 1 || page.Sources[0].Seen != 2 || len(page.Sources[0].Nights) != 2 || page.Sources[0].Nights[0].Night != "2025-03-02" {
		t.Errorf("vetted source %+v", page.Sources)
	}
}

func TestDiscoverySourceWatching(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	if err := a.db.AutoMigrate(&app.DiscoveryWatch{}, &app.DiscoveryWatchNight{}); err != nil {
		t.Fatal(err)
	}
	var page DiscoverySpotList
	a.get("/api/v1/discoveries/sources?since=all&view=look", &page)
	id := page.Sources[0].ID
	if err := a.db.Create(&app.DiscoveryVet{SourceID: id, State: "done", Suggested: app.VetReal, Confidence: "low"}).Error; err != nil {
		t.Fatal(err)
	}
	a.get("/api/v1/discoveries/sources?since=all", &page)
	if page.Counts.Watching != 1 || page.Counts.Look != 0 {
		t.Fatalf("real at low confidence with nothing pending belongs in Watching: %+v", page.Counts)
	}
	now := time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC)
	svc := &discoverywatch.Service{DB: a.db, Now: func() time.Time { return now }}
	applyWatchRoutes(a.r.Group("/api/v1"), svc)
	base := "/api/v1/discoveries/sources/" + strconv.Itoa(id)
	if w := a.do(http.MethodPost, base+"/watch/stop", nil); w.Code != http.StatusConflict {
		t.Errorf("stop before the watch started: %d", w.Code)
	}
	if err := svc.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.get("/api/v1/discoveries/sources?since=all&view=watching", &page)
	if len(page.Sources) != 1 || !page.Sources[0].Watching || page.Sources[0].Look || page.Sources[0].Watch == nil ||
		page.Sources[0].Watch.Imaging != discoverywatch.ImagingAsking || len(page.Sources[0].Watch.Nights) != discoverywatch.NightsAsked {
		t.Fatalf("watching view %+v", page.Sources)
	}
	w := a.do(http.MethodPost, base+"/watch/stop", nil)
	var v discoverywatch.View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != http.StatusOK || v.Imaging != discoverywatch.ImagingStoppedUser {
		t.Fatalf("stop %d %s", w.Code, w.Body)
	}
	for _, n := range v.Nights {
		if n.State != app.EclipseCancel {
			t.Errorf("night %s still %s", n.Night, n.State)
		}
	}
}
