package bodies_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/bodies"
	"github.com/USA-RedDragon/astro-stacker/internal/sky"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
)

func cometList(t *testing.T, fail *atomic.Bool, hits *atomic.Int32, now *time.Time) *bodies.CometList {
	t.Helper()
	els, sbdb := readFile(t, "CometEls-sample.txt"), readFile(t, "sbdb-sample.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		if r.URL.Path == "/els" {
			hits.Add(1)
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/sbdb" {
			_, _ = w.Write([]byte(sbdb))
			return
		}
		_, _ = w.Write([]byte(els))
	}))
	t.Cleanup(srv.Close)
	db := open(t)
	if err := db.AutoMigrate(&app.CometElements{}); err != nil {
		t.Fatal(err)
	}
	return &bodies.CometList{DB: db, URL: srv.URL + "/els", SBDBURL: srv.URL + "/sbdb", Now: func() time.Time { return *now },
		Site: func(context.Context) (sky.Site, error) { return testSite(), nil }}
}

func TestCometListTonight(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	var hits atomic.Int32
	now := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	c := cometList(t, &fail, &hits, &now)
	ctx := context.Background()
	all, err := c.List(ctx, 99, alt(30), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Comets) != 9 || all.Elements.Comets != 9 || all.Elements.FetchedAt == nil || all.Night.Dusk == nil {
		t.Fatalf("all: %d comets, elements %+v", len(all.Comets), all.Elements)
	}
	if all.JPLMags.Comets != 9 || all.JPLMags.FetchedAt == nil {
		t.Errorf("JPL magnitudes %+v", all.JPLMags)
	}
	for i := 1; i < len(all.Comets); i++ {
		a, oka := all.Comets[i-1].Brightest()
		b, okb := all.Comets[i].Brightest()
		if oka && okb && a > b || !oka && okb {
			t.Errorf("not brightest first: %v then %v", a, b)
		}
	}
	checkSwan(t, all.Comets)
	listed, err := c.List(ctx, 15, alt(30), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, cm := range listed.Comets {
		if m, ok := cm.Brightest(); !ok || m > 15 || cm.Window.Hours == 0 {
			t.Errorf("listed %s mag %v hours %v", cm.Designation, m, cm.Window.Hours)
		}
	}
	if len(listed.Comets) >= len(all.Comets) {
		t.Errorf("the magnitude and altitude limits removed nothing")
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches, want 1 a day", hits.Load())
	}

	now = now.Add(25 * time.Hour)
	fail.Store(true)
	stale, err := c.List(ctx, 99, alt(30), true)
	if err != nil || stale.Elements.Error == "" || len(stale.Comets) != 9 {
		t.Fatalf("stale fallback: %v %+v", err, stale.Elements)
	}
	if hits.Load() != 2 {
		t.Fatalf("%d fetches after a day", hits.Load())
	}
}

func TestCometListWithoutElements(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	var hits atomic.Int32
	fail.Store(true)
	now := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	c := cometList(t, &fail, &hits, &now)
	if _, err := c.List(context.Background(), 15, nil, false); err == nil {
		t.Fatal("listed comets with no elements")
	}
}

func checkSwan(t *testing.T, comets []bodies.Comet) {
	t.Helper()
	var r2 *bodies.Comet
	for i := range comets {
		if comets[i].Designation == swan {
			r2 = &comets[i]
		}
	}
	if r2 == nil || r2.Command != "DES=C/2025 R2;CAP;NOFRAG" || r2.MagMPC == nil || *r2.MagMPC < 23 || *r2.MagMPC > 24 ||
		r2.MagJPL == nil || *r2.MagJPL < 24 || *r2.MagJPL > 24.5 {
		t.Fatalf("C/2025 R2 %+v", r2)
	}
	if r2.MotionRate < 0.2 || r2.MotionRate > 0.3 {
		t.Errorf("C/2025 R2 moves %v arcsec/min; Horizons gives about 0.22", r2.MotionRate)
	}
	if r2.Window.Hours <= 0 || r2.Window.Start == nil || r2.Window.PeakAlt < 60 {
		t.Errorf("C/2025 R2 should be high late tonight: %+v", r2.Window)
	}
}

func alt(v float64) *float64 { return &v }

func TestCometListAltitudeAndJSON(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	var hits atomic.Int32
	now := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC)
	c := cometList(t, &fail, &hits, &now)
	ctx := context.Background()
	def, err := c.List(ctx, 99, nil, false)
	if err != nil || def.MinAltitude != bodies.DefaultMinAlt {
		t.Fatalf("default altitude %v %v", def.MinAltitude, err)
	}
	low, err := c.List(ctx, 99, alt(0), false)
	if err != nil || low.MinAltitude != 0 || len(low.Comets) < len(def.Comets) {
		t.Fatalf("min_alt 0: %v, %d comets against %d at 30", err, len(low.Comets), len(def.Comets))
	}
	for _, at := range []time.Time{now, now.AddDate(16, 0, 0), now.AddDate(-60, 0, 0)} {
		now = at
		all, err := c.List(ctx, 99, alt(0), true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(all); err != nil {
			t.Fatalf("at %v the list does not encode: %v", at, err)
		}
	}
	if _, err := c.List(ctx, math.NaN(), nil, false); !errors.Is(err, bodies.ErrBadQuery) {
		t.Errorf("NaN max_mag: %v", err)
	}
}
