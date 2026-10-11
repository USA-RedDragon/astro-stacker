package observatory_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"gorm.io/gorm"
)

const (
	pathHorizon = "/os/v1/horizon"
	profile1    = "prof-1"
	modeDown    = "down"
)

func southHorizon() []float64 {
	alts := make([]float64, 360)
	for i := range alts {
		if i >= 100 && i <= 260 {
			alts[i] = 40
		}
	}
	return alts
}

func horizonServer(t *testing.T, mode *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.URL.Path != pathHorizon {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m, _ := mode.Load().(string)
		switch m {
		case "custom":
			_ = json.NewEncoder(w).Encode(map[string]any{"custom": true, "profile_id": profile1, "azimuth_step": 1, "altitudes": southHorizon()})
		case "none":
			_, _ = w.Write([]byte(`{"custom":false,"profile_id":"prof-1"}`))
		case "short":
			_, _ = w.Write([]byte(`{"custom":true,"profile_id":"prof-1","azimuth_step":1,"altitudes":[1,2,3]}`))
		case "old":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientHorizon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mode := &atomic.Value{}
	c := observatory.NewClient(horizonServer(t, mode).URL, token)
	mode.Store("custom")
	h, err := c.Horizon(ctx)
	if err != nil || !h.Custom || h.ProfileID != profile1 || h.AzimuthStep != 1 || len(h.Altitudes) != 360 || h.Altitudes[180] != 40 {
		t.Fatalf("custom %+v %v", h, err)
	}
	mode.Store("none")
	if h, err := c.Horizon(ctx); err != nil || h.Custom || h.ProfileID != profile1 || h.Altitudes != nil {
		t.Fatalf("no horizon %+v %v", h, err)
	}
	mode.Store("old")
	if _, err := c.Horizon(ctx); !errors.Is(err, observatory.ErrHorizonUnsupported) {
		t.Fatalf("404 %v", err)
	}
	mode.Store("short")
	if _, err := c.Horizon(ctx); !errors.Is(err, observatory.ErrHorizonInvalid) {
		t.Fatalf("short horizon %v", err)
	}
	mode.Store(modeDown)
	if _, err := c.Horizon(ctx); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("502 %v", err)
	}
}

func TestHorizonProviderKeepsAndPersistsTheLastGoodAnswer(t *testing.T) {
	t.Parallel()
	checkHorizonProvider(t, memDB(t, "horizon"))
}

func checkHorizonProvider(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	if err := db.AutoMigrate(&app.ObservatoryHorizon{}); err != nil {
		t.Fatal(err)
	}
	mode := &atomic.Value{}
	mode.Store(modeDown)
	c := observatory.NewClient(horizonServer(t, mode).URL, token)
	at := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	p := &observatory.HorizonProvider{Client: c, DB: db, Now: func() time.Time { return at }}
	if err := p.Refresh(ctx); err == nil {
		t.Fatal("refresh against a down PC succeeded")
	}
	if _, ok := p.Get(ctx); ok {
		t.Fatal("a report appeared without an answer")
	}
	mode.Store("custom")
	if err := p.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{modeDown, "old", "short"} {
		mode.Store(m)
		_ = p.Refresh(ctx)
		r, ok := p.Get(ctx)
		if !ok || !r.Custom || r.Horizon == nil || r.Horizon.At(180) != 40 || r.Horizon.At(10) != 0 || !r.FetchedAt.Equal(at) {
			t.Fatalf("after %s the last good report was lost: %+v %v", m, r, ok)
		}
	}
	restarted := &observatory.HorizonProvider{Client: c, DB: db}
	r, ok := restarted.Get(ctx)
	if !ok || r.ProfileID != profile1 || r.Horizon == nil || r.Horizon.At(180) != 40 || !r.FetchedAt.Equal(at) {
		t.Fatalf("restart lost the saved horizon: %+v %v", r, ok)
	}
	mode.Store("none")
	if err := restarted.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if r, ok := (&observatory.HorizonProvider{DB: db}).Get(ctx); !ok || r.Custom || r.Horizon != nil {
		t.Fatalf("a profile without a horizon was not saved: %+v %v", r, ok)
	}
	var n int64
	db.Model(&app.ObservatoryHorizon{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d rows for one profile", n)
	}
}
