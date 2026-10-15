package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/discoveries"
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
