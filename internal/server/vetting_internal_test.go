package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/USA-RedDragon/astro-stacker/internal/vetting"
)

func TestSourceChecksQueueAndRead(t *testing.T) {
	t.Parallel()
	a := newDiscoveryAPI(t)
	if err := a.db.AutoMigrate(&app.DiscoveryVet{}); err != nil {
		t.Fatal(err)
	}
	var page DiscoverySpotList
	a.get("/api/v1/discoveries/sources?since=all&view=look", &page)
	if len(page.Sources) != 1 {
		t.Fatalf("sources %+v", page)
	}
	base := "/api/v1/discoveries/sources/" + strconv.Itoa(page.Sources[0].ID) + "/checks"
	var st vetting.Status
	if code := a.get(base, &st); code != http.StatusOK || st.State != vetting.StateNone || st.Report != nil {
		t.Fatalf("before any run %d %+v", code, st)
	}
	w := a.do(http.MethodPost, base, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK || st.State != vetting.StateQueued || st.RequestedAt == nil {
		t.Fatalf("queued %d %s", w.Code, w.Body)
	}
	if w := a.do(http.MethodPost, base, nil); w.Code != http.StatusOK {
		t.Fatalf("queued again %d %s", w.Code, w.Body)
	}
	if code := a.get("/api/v1/discoveries/sources/999/checks", &st); code != http.StatusOK || st.State != vetting.StateNone {
		t.Errorf("unknown source reads as not run: %d", code)
	}
	if w := a.do(http.MethodPost, "/api/v1/discoveries/sources/999/checks", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown source can't be queued: %d", w.Code)
	}
}
