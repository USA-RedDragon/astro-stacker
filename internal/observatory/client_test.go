package observatory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/coder/websocket"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	token        = "secret"
	statusJSON   = `{"version":"5.8.2.101","state":"imaging","paused":false,"target":{"project_id":5,"project_name":"Sadr Region","target_id":13,"target_name":"IC 1318 Panel 15","priority":2,"is_mosaic":true},"exposure":{"filter":"R","seconds":600,"started_at":"2026-10-10T06:40:00Z","ends_at":"2026-10-10T06:50:00Z"},"scores":[{"rule":"Project Priority","weight":0.5,"score":0.5}],"web_editing":true}`
	pathStatus   = "/os/v1/status"
	pathCommands = "/os/v1/commands"
	stateImaging = "imaging"
)

func authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+token {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func memDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name()+name, "/", "_"), time.Now().UnixNano())), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestClientStatusAndAuth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.URL.Path == pathStatus {
			_, _ = io.WriteString(w, statusJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s, err := observatory.NewClient(srv.URL+"/", token).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.State != stateImaging || s.Target == nil || s.Target.TargetID != 13 || s.Exposure == nil || s.Exposure.EndsAt == nil || len(s.Scores) != 1 {
		t.Fatalf("status %+v", s)
	}

	_, err = observatory.NewClient(srv.URL, "wrong").Status(context.Background())
	if !errors.Is(err, observatory.ErrUnauthorized) || errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("bad token: %v", err)
	}
}

func TestClientUnreachable(t *testing.T) {
	t.Parallel()
	disabled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer disabled.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	env := schedcmd.Envelope{ID: "a", Kind: schedcmd.KindResume}
	for name, c := range map[string]*observatory.Client{
		"unconfigured": observatory.NewClient("", token),
		"disabled":     observatory.NewClient(disabled.URL, token),
		"closed":       observatory.NewClient(closed.URL, token),
	} {
		if _, err := c.Send(context.Background(), env); !errors.Is(err, schedcmd.ErrUnreachable) {
			t.Errorf("%s: %v, want unreachable", name, err)
		}
	}
}

func TestClientCommandsAndPreview(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == pathCommands:
			_, _ = io.WriteString(w, `{"id":"a","status":"pending","applies_at":"2026-10-10T06:50:00Z"}`)
		case r.Method == http.MethodGet && r.URL.Path == pathCommands+"/a":
			_, _ = io.WriteString(w, `{"id":"a","status":"applied"}`)
		case r.Method == http.MethodDelete && r.URL.Path == pathCommands+"/a":
			_, _ = io.WriteString(w, `{"id":"a","status":"cancelled"}`)
		case r.URL.Path == "/os/v1/preview":
			_, _ = io.WriteString(w, `{"blocks":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := observatory.NewClient(srv.URL, token)
	ctx := context.Background()

	tr := observatory.NewAPITransport(c)
	r, err := tr.Send(ctx, schedcmd.Envelope{ID: "a", Kind: schedcmd.KindResume, Payload: json.RawMessage(`{}`)})
	if err != nil || r.Status != schedcmd.StatusPending || r.AppliesAt == nil {
		t.Fatalf("send: %+v %v", r, err)
	}
	if r, err := c.Command(ctx, "a"); err != nil || r.Status != schedcmd.StatusApplied {
		t.Fatalf("get: %+v %v", r, err)
	}
	if r, err := tr.Cancel(ctx, "a"); err != nil || r.Status != schedcmd.StatusCancelled {
		t.Fatalf("cancel: %+v %v", r, err)
	}
	if _, err := c.Command(ctx, "missing"); !errors.Is(err, observatory.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	start := time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC)
	if raw, err := c.Preview(ctx, &start); err != nil || string(raw) != `{"blocks":[]}` {
		t.Fatalf("preview: %s %v", raw, err)
	}
	v := json.RawMessage(`2`)
	if _, err := c.PreviewWith(ctx, observatory.PreviewRequest{Overrides: []observatory.Override{{Entity: "project", ID: 5, Field: "priority", Value: v}}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		`POST /os/v1/commands {"id":"a","kind":"scheduler.resume"`,
		"GET /os/v1/preview?start=2026-10-10T17%3A00%3A00Z",
		`POST /os/v1/preview {"overrides":[{"entity":"project","id":5,"field":"priority","value":2}]}`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

type sink struct {
	mu      sync.Mutex
	results []schedcmd.Result
}

func (s *sink) ApplyResult(_ context.Context, r schedcmd.Result) (schedcmd.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = append(s.results, r)
	return schedcmd.Record{ID: r.ID, Status: r.Status}, nil
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

type broker struct {
	mu   sync.Mutex
	sent []any
}

func (b *broker) Broadcast(typ string, v any, sticky bool) {
	if typ != "scheduler" || !sticky {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, v)
}

func (b *broker) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sent)
}

func TestMonitorWebSocket(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		if r.URL.Path != "/os/v1/ws" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"status","data":`+statusJSON+`}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`not json`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"result","data":{"id":"cmd-1","status":"applied"}}`))
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer srv.Close()

	s := &sink{}
	b := &broker{}
	online := make(chan struct{}, 1)
	m := observatory.NewMonitor(observatory.NewClient(srv.URL, token), s, b)
	m.OnOnline = func() { online <- struct{}{} }
	if err := m.Listen(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := m.View()
	if v.Reachable != observatory.Online || v.Status == nil || v.State != stateImaging || v.LastAnswer == nil || v.Since == nil {
		t.Fatalf("view %+v", v)
	}
	if s.count() != 1 || s.results[0].ID != "cmd-1" || s.results[0].Status != schedcmd.StatusApplied {
		t.Fatalf("results %+v", s.results)
	}
	if b.count() == 0 {
		t.Fatal("no scheduler broadcast")
	}
	select {
	case <-online:
	case <-time.After(2 * time.Second):
		t.Fatal("OnOnline not called")
	}
	raw, err := json.Marshal(m.Status())
	if err != nil || !strings.Contains(string(raw), `"reachable":"online"`) || !strings.Contains(string(raw), `"target_name":"IC 1318 Panel 15"`) {
		t.Fatalf("json %s %v", raw, err)
	}
}

func TestMonitorPollAndStates(t *testing.T) {
	t.Parallel()
	up := true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := up
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == pathStatus {
			_, _ = io.WriteString(w, statusJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := &broker{}
	m := observatory.NewMonitor(observatory.NewClient(srv.URL, token), nil, b)
	if v := m.View(); v.Reachable != observatory.Offline {
		t.Fatalf("before any answer: %s", v.Reachable)
	}
	if err := m.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := m.View(); v.Reachable != observatory.Online || v.Live {
		t.Fatalf("after poll: %+v", v)
	}
	mu.Lock()
	up = false
	mu.Unlock()
	if err := m.Poll(context.Background()); !errors.Is(err, schedcmd.ErrUnreachable) {
		t.Fatalf("poll down: %v", err)
	}
	v := m.View()
	if v.Reachable != observatory.Offline || v.Status == nil || v.Error == "" {
		t.Fatalf("after failure: %+v", v)
	}

	u := observatory.NewMonitor(observatory.NewClient("", ""), nil, b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	u.Run(ctx)
	if v := u.View(); v.Reachable != observatory.Unconfigured {
		t.Fatalf("unconfigured: %+v", v)
	}
}

func TestMonitorRunReconnects(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	conns := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathStatus:
			_, _ = io.WriteString(w, statusJSON)
		case "/os/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			mu.Lock()
			conns++
			mu.Unlock()
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"status","data":`+statusJSON+`}`))
			_ = conn.Close(websocket.StatusGoingAway, "restart")
		}
	}))
	defer srv.Close()
	m := observatory.NewMonitor(observatory.NewClient(srv.URL, token), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	m.Run(ctx)
	mu.Lock()
	defer mu.Unlock()
	if conns < 2 {
		t.Fatalf("connected %d times, want a reconnect", conns)
	}
	if m.View().Reachable != observatory.Online {
		t.Fatalf("view %+v", m.View())
	}
}

func TestClientSendWaitsPastRequestTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		time.Sleep(observatory.RequestTimeout + 500*time.Millisecond)
		_, _ = io.WriteString(w, `{"id":"c1","status":"pending"}`)
	}))
	defer srv.Close()

	r, err := observatory.NewClient(srv.URL, token).Send(context.Background(), schedcmd.Envelope{ID: "c1", Kind: "scheduler.resume"})
	if err != nil {
		t.Fatalf("slow send: %v", err)
	}
	if r.Status != schedcmd.StatusPending {
		t.Fatalf("status %q", r.Status)
	}
}
