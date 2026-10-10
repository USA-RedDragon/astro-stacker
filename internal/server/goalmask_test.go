package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/config"
	"github.com/USA-RedDragon/astro-stacker/internal/goals"
	"github.com/USA-RedDragon/astro-stacker/internal/server/middleware"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/gin-gonic/gin"
)

func goalMaskEngine(t *testing.T) *gin.Engine {
	t.Helper()
	appStore := memStore(t)
	if err := appStore.DB().AutoMigrate(&app.GoalMask{}); err != nil {
		t.Fatal(err)
	}
	bits := []uint8{
		0, goals.MaskCovered, goals.MaskCovered | goals.MaskBand,
		goals.MaskCovered | goals.MaskStar, goals.MaskCovered | goals.MaskSky, goals.MaskCovered | goals.MaskBand,
	}
	data, err := goals.CompressMask(bits)
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := 1.5, 3.0
	gm := app.GoalMask{Object: objectVeil, Filter: filterHa, Width: 3, Height: 2, Bin: goals.NoiseBin, FrameWidth: 13, FrameHeight: 9,
		Source: goals.NoiseMaskFaint, NoiseMask: goals.NoiseMaskFaint, Sky: 0.5, BandLo: &lo, BandHi: &hi,
		Covered: 5, Band: 2, Stars: 1, SkyPixels: 1, Subs: 40, Data: data, MeasuredAt: time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)}
	if err := appStore.DB().Create(&gm).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(middleware.Inject(&middleware.DepInjection{Config: &config.Config{}, AppStore: appStore, SchedulerDBStore: memStore(t)}))
	applyPlanningRoutes(r.Group("/api/v1"))
	return r
}

func serve(r *gin.Engine, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGoalMaskInfoRoute(t *testing.T) {
	t.Parallel()
	r := goalMaskEngine(t)
	w := serve(r, "/api/v1/goals/mask/info?object=Veil&filter=H-a")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var info GoalMaskInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Width != 3 || info.Height != 2 || info.FrameWidth != 13 || info.BandPixels != 2 || info.CoveredPixels != 5 ||
		info.BandPct != 40 || info.StarPct != 20 || info.TotalPixels != 6 || info.BandLowPct != goals.BandLowPercentile ||
		info.BandHighPct != goals.BandHighPercent || *info.BandLo != 1.5 || info.Subs != 40 {
		t.Fatalf("info %+v", info)
	}
	if w := serve(r, "/api/v1/goals/mask/info?object=Veil&filter=O-III"); w.Code != http.StatusNotFound {
		t.Errorf("unmeasured filter: %d", w.Code)
	}
	if w := serve(r, "/api/v1/goals/mask/info?object=Veil"); w.Code != http.StatusBadRequest {
		t.Errorf("no filter: %d", w.Code)
	}
}

func TestGoalMaskRoute(t *testing.T) {
	t.Parallel()
	r := goalMaskEngine(t)
	for layer, want := range map[string][]bool{
		"band":  {false, false, true, false, false, true},
		"stars": {false, false, false, true, false, false},
		"sky":   {false, false, false, false, true, false},
	} {
		w := serve(r, "/api/v1/goals/mask?object=Veil&filter=H-a&layer="+layer)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("%s: %d %s", layer, w.Code, w.Body)
		}
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
			t.Fatalf("%s: %v", layer, b)
		}
		for i, on := range want {
			_, _, _, a := img.At(i%3, i/3).RGBA()
			if (a == 0xffff) != on || (a != 0 && a != 0xffff) {
				t.Errorf("%s pixel %d alpha %d, want %v", layer, i, a, on)
			}
		}
		if again := serve(r, "/api/v1/goals/mask?object=Veil&filter=H-a&layer="+layer, "If-None-Match", w.Header().Get("ETag")); again.Code != http.StatusNotModified {
			t.Errorf("%s: conditional request %d", layer, again.Code)
		}
	}
	if w := serve(r, "/api/v1/goals/mask?object=Veil&filter=H-a&layer=nebula"); w.Code != http.StatusBadRequest {
		t.Errorf("unknown layer: %d", w.Code)
	}
	if w := serve(r, "/api/v1/goals/mask?object=M31&filter=H-a"); w.Code != http.StatusNotFound {
		t.Errorf("unmeasured: %d", w.Code)
	}
}
