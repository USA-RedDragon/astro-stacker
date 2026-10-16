package conditions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHistoryStepKeepsPointsBounded(t *testing.T) {
	t.Parallel()
	for minutes, want := range map[int]time.Duration{5: 15 * time.Second, 60: 15 * time.Second, 61: 16 * time.Second, 120: 30 * time.Second, 360: 90 * time.Second} {
		got := HistoryStep(minutes)
		if got != want {
			t.Errorf("%d minutes: step %v, want %v", minutes, got, want)
		}
		if n := int(time.Duration(minutes)*time.Minute/got) + 1; n > HistoryMaxPoints+1 {
			t.Errorf("%d minutes: %d points", minutes, n)
		}
	}
}

func TestCameraHistoryMergesSeriesByTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 7, 0, time.UTC)
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, q.Get("query")+" "+q.Get("start")+" "+q.Get("end")+" "+q.Get("step"))
		mu.Unlock()
		var values string
		switch {
		case strings.Contains(q.Get("query"), "observatory_camera_temperature_celsius"):
			values = `[1792065600,"-9.8"],[1792065615,"-10"]`
		case strings.Contains(q.Get("query"), "target_temperature"):
			values = `[1792065600,"-10"],[1792065615,"-10"]`
		default:
			values = `[1792065615,"41.5"],[1792065630,"NaN"]`
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + values + `]}]}}`))
	}))
	defer srv.Close()
	s := New(Options{MetricsURL: srv.URL}, nil)
	s.now = func() time.Time { return now }
	h := s.CameraHistory(context.Background(), 60)
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"source":"prometheus","step_seconds":15,"points":[` +
		`{"t":"2026-10-15T12:00:00Z","temperature":-9.8,"setpoint":-10},` +
		`{"t":"2026-10-15T12:00:15Z","temperature":-10,"setpoint":-10,"cooler_power":41.5}]}`
	if string(b) != want {
		t.Fatalf("history\n got %s\nwant %s", b, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 3 || queries[0] != "max(observatory_camera_temperature_celsius) 1792062000 1792065600 15" {
		t.Fatalf("queries %q", queries)
	}
}

func TestCameraHistoryWithoutMetrics(t *testing.T) {
	t.Parallel()
	h := New(Options{}, nil).CameraHistory(context.Background(), 360)
	if h.Source.Source != SourceNone || h.Error == "" || h.StepSeconds != 90 || h.Points == nil || len(h.Points) != 0 {
		t.Fatalf("no metrics URL %+v", h)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()
	h = New(Options{MetricsURL: srv.URL}, nil).CameraHistory(context.Background(), 60)
	if h.Source.Source != SourceError || !strings.Contains(h.Error, "bad query") || len(h.Points) != 0 {
		t.Fatalf("failed query %+v", h)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer empty.Close()
	h = New(Options{MetricsURL: empty.URL}, nil).CameraHistory(context.Background(), 60)
	if h.Source.Source != SourceNone || h.Error == "" || h.Points == nil {
		t.Fatalf("no series %+v", h)
	}
}
