package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/server"
)

func TestAutofocusHistory(t *testing.T) {
	t.Parallel()
	sched := memDB(t, "s")
	h := newHandler(t, sched, memDB(t, "a"), server.Extras{Now: func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }})
	const path = "/api/v1/scheduler/autofocus"
	code, body := do(t, h, http.MethodGet, path, "")
	if code != http.StatusOK || !strings.Contains(string(body), `"available":false`) || !strings.Contains(string(body), `"runs":[]`) ||
		!strings.Contains(string(body), `"since":"2026-10-05T12:00:00Z"`) {
		t.Fatalf("%d %s", code, body)
	}
	exec(t, sched, `CREATE TABLE ts_autofocus_run (id TEXT NOT NULL PRIMARY KEY, profile_id TEXT, started_at TIMESTAMP, finished_at TIMESTAMP NOT NULL,
		duration_seconds REAL, success INTEGER NOT NULL, error TEXT, filter TEXT, temperature REAL, autofocuser TEXT, method TEXT, fitting TEXT,
		initial_position REAL, previous_position REAL, previous_value REAL, chosen_position REAL, chosen_value REAL, points TEXT NOT NULL, fit TEXT,
		target_name TEXT, target_guid TEXT, source TEXT NOT NULL, recorded_at TIMESTAMP NOT NULL)`,
		`INSERT INTO ts_autofocus_run (id, started_at, finished_at, success, filter, chosen_position, chosen_value, points, source, recorded_at) VALUES
		('old', '2026-09-01 01:00:00', '2026-09-01 01:02:00', 1, 'R', 5100, 2.0, '[]', 'event', '2026-09-01 01:02:00'),
		('new', '2026-10-12 03:00:00', '2026-10-12 03:02:00', 1, 'L', 5040, 1.9, '[{"position":5000,"value":2.1,"error":0.1}]', 'event', '2026-10-12 03:02:00')`)
	code, body = do(t, h, http.MethodGet, path, "")
	if code != http.StatusOK || !strings.Contains(string(body), `"available":true`) || !strings.Contains(string(body), `"id":"new"`) ||
		strings.Contains(string(body), `"id":"old"`) || !strings.Contains(string(body), `"points":[{"position":5000,"value":2.1,"error":0.1}]`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do(t, h, http.MethodGet, path+"?since=2026-08-01T00:00:00Z", "")
	if code != http.StatusOK || strings.Index(string(body), `"id":"new"`) > strings.Index(string(body), `"id":"old"`) || !strings.Contains(string(body), `"id":"old"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, q := range []string{"?since=yesterday", "?limit=0", "?limit=x", "?limit=1001"} {
		if code, body = do(t, h, http.MethodGet, path+q, ""); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, code, body)
		}
	}
}

func TestSchedulerStatusCarriesTheLastAutofocus(t *testing.T) {
	t.Parallel()
	const af = `"autofocus":{"run_id":"r1","state":"finished","started_at":"2026-10-12T03:00:00Z","finished_at":"2026-10-12T03:02:00Z","filter":"L","autofocuser":"Hocus Focus",` +
		`"chosen_position":5041.7,"chosen_value":1.92,"points":[{"position":4900,"value":3.2,"error":0.1},{"position":5000,"value":2.4}],"fit":{"r_squared":{"Hyperbolic":0.98}}}`
	var s observatory.Status
	if err := json.Unmarshal([]byte(`{"state":"imaging",`+af+`}`), &s); err != nil {
		t.Fatal(err)
	}
	m := observatory.NewMonitor(observatory.NewClient("http://127.0.0.1:1", "x"), nil, nil)
	m.SetStatus(s, true)
	h := newHandler(t, memDB(t, "s"), memDB(t, "a"), server.Extras{Scheduler: m})
	code, body := do(t, h, http.MethodGet, "/api/v1/scheduler/status", "")
	if code != http.StatusOK || !strings.Contains(string(body), af) {
		t.Fatalf("%d %s", code, body)
	}
}
