package halpha_test

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/halpha"
	"github.com/USA-RedDragon/astro-stacker/internal/imagedata"
	"github.com/USA-RedDragon/astro-stacker/internal/store/models/app"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const testW, testH = 36, 18

func carFITS(t *testing.T, value func(col, rowFromBottom int) float32) []byte {
	t.Helper()
	data := make([]float32, testW*testH)
	for top := range testH {
		for col := range testW {
			data[top*testW+col] = value(col, testH-1-top)
		}
	}
	cards := []imagedata.Card{
		imagedata.FloatCard("CRPIX1", testW/2, ""), imagedata.FloatCard("CRPIX2", testH/2, ""),
		imagedata.FloatCard("CDELT1", -360.0/testW, ""), imagedata.FloatCard("CDELT2", 180.0/testH, ""),
		imagedata.FloatCard("CRVAL1", 180, ""), imagedata.FloatCard("CRVAL2", 0, ""),
		imagedata.StringCard("CTYPE1", "RA---CAR", ""), imagedata.StringCard("CTYPE2", "DEC--CAR", ""),
	}
	var buf bytes.Buffer
	if err := imagedata.WriteFITS(&buf, testW, testH, 1, data, cards); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSampleFollowsTheCARProjection(t *testing.T) {
	t.Parallel()
	byRow, err := halpha.FromFITS(carFITS(t, func(_, row int) float32 { return float32(row*10 + 1) }))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := byRow.Sample(0, 40, 0); !ok || s.Rayleigh != 121 || s.Peak != 121 {
		t.Errorf("dec 40: %+v %v", s, ok)
	}
	if s, ok := byRow.Sample(0, -80, 0); !ok || s.Rayleigh != 1 {
		t.Errorf("dec -80: %+v %v", s, ok)
	}
	if s, ok := byRow.Sample(0, 40, 15); !ok || s.Peak <= s.Rayleigh || s.Rayleigh < 100 || s.RadiusDeg != 15 {
		t.Errorf("area sample %+v", s)
	}
	byCol, err := halpha.FromFITS(carFITS(t, func(col, _ int) float32 { return float32(col + 1) }))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := byCol.Sample(170, 0, 0); s.Rayleigh != 19 {
		t.Errorf("ra 170: %+v", s)
	}
	a, _ := byCol.Sample(359.9, 0, 0)
	b, _ := byCol.Sample(-0.1, 0, 0)
	if a != b {
		t.Errorf("ra wrap %+v %+v", a, b)
	}
	if _, ok := (*halpha.Map)(nil).Sample(1, 1, 1); ok {
		t.Error("nil map sampled")
	}
}

func TestLabelsAndScores(t *testing.T) {
	t.Parallel()
	if halpha.Label(nil) != "Unknown" || halpha.Score(nil) != 0 {
		t.Error("unknown")
	}
	cases := map[float64]string{1: "Little H-α (<2 R)", 4: "Faint H-α (4 R)", 12: "H-α (12 R)", 300: "Strong H-α (300 R)"}
	for r, want := range cases {
		if got := halpha.Label(&halpha.Sample{Rayleigh: r}); got != want {
			t.Errorf("%v R: %q", r, got)
		}
	}
	if s := halpha.Score(&halpha.Sample{Rayleigh: 500}); s != 1 {
		t.Errorf("score at 500 R %v", s)
	}
	if lo, hi := halpha.Score(&halpha.Sample{Rayleigh: 10}), halpha.Score(&halpha.Sample{Rayleigh: 100}); !(lo > 0 && hi > lo && hi < 1) {
		t.Errorf("scores %v %v", lo, hi)
	}
}

func TestProviderFetchesOnceAndCaches(t *testing.T) {
	t.Parallel()
	body := carFITS(t, func(col, row int) float32 { return float32(math.Pow(1.5, float64(row%12))) + float32(col)/100 })
	var hits atomic.Int32
	var agent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		agent.Store(r.UserAgent())
		if r.URL.Query().Get("hips") != halpha.SurveyID || r.URL.Query().Get("projection") != "CAR" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&app.HAlphaMap{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &halpha.Provider{DB: db, Endpoint: srv.URL}
	if _, st := p.Get(); st.State != halpha.StateLoading || st.FetchedAt != nil {
		t.Errorf("before loading %+v", st)
	}
	p.Run(ctx)
	m, st := p.Get()
	if st.State != halpha.StateReady || m == nil || hits.Load() != 1 || agent.Load() != halpha.UserAgent {
		t.Fatalf("state %+v hits %d agent %v", st, hits.Load(), agent.Load())
	}
	fresh, err := halpha.FromFITS(body)
	if err != nil {
		t.Fatal(err)
	}
	cached := &halpha.Provider{DB: db, Endpoint: srv.URL}
	cached.Run(ctx)
	cm, cst := cached.Get()
	if cst.State != halpha.StateReady || hits.Load() != 1 || cst.FetchedAt == nil {
		t.Fatalf("cache not used: %+v hits %d", cst, hits.Load())
	}
	for _, pos := range [][2]float64{{10, 20}, {200, -45}, {300, 70}} {
		want, _ := fresh.Sample(pos[0], pos[1], 3)
		got, _ := cm.Sample(pos[0], pos[1], 3)
		if math.Abs(got.Rayleigh-want.Rayleigh) > 0.01*want.Rayleigh+0.1 {
			t.Errorf("cached map at %v: %+v, fetched %+v", pos, got, want)
		}
	}
	if off := (&halpha.Provider{Off: true}); true {
		off.Run(ctx)
		if _, st := off.Get(); st.State != halpha.StateOff {
			t.Errorf("off %+v", st)
		}
	}
}

func TestProviderFailsGracefully(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p := &halpha.Provider{Endpoint: srv.URL}
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if m, st := p.Get(); st.State == halpha.StateFailed {
			if m != nil || st.Error == nil {
				t.Errorf("failed state %+v", st)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("never failed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
