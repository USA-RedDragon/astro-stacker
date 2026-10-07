package events

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStreamReplaysAfterLastEventID(t *testing.T) {
	b := NewBroker()
	b.Publish(Event{Type: TypeMaster, Object: "M31", Filter: "Red"})
	b.Publish(Event{Type: TypePreview, Object: "M31", Key: "a"})
	first := b.recent[0].ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(b)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	req.Header.Set("Last-Event-ID", strconv.FormatUint(first, 10))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string, 16)
	go func() {
		s := bufio.NewScanner(res.Body)
		for s.Scan() {
			if strings.HasPrefix(s.Text(), "data: ") {
				lines <- s.Text()
			}
		}
	}()
	want := func(sub string) {
		select {
		case l := <-lines:
			if !strings.Contains(l, sub) {
				t.Fatalf("got %s, want %s", l, sub)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no event with %s", sub)
		}
	}
	// Only the event after Last-Event-ID is replayed, then live ones follow.
	want(`"type":"preview"`)
	time.Sleep(50 * time.Millisecond)
	b.Publish(Event{Type: TypeMaster, Object: "M33", Filter: "H-a"})
	want(`"object":"M33"`)
	b.SetStatus(Status{Workers: []Worker{{Object: "M42", Stage: "registering", Done: 3, Total: 12}}})
	want(`"type":"status"`)
}

func TestNewListenerGetsLatestStatusFirst(t *testing.T) {
	b := NewBroker()
	b.SetStatus(Status{Backlog: Backlog{LightsPending: 1}})
	b.SetStatus(Status{Backlog: Backlog{LightsPending: 2}})
	backlog, _, cancel := b.Subscribe(0)
	defer cancel()
	if len(backlog) != 1 || !strings.Contains(string(backlog[0]), `"lights_pending":2`) {
		t.Fatalf("backlog %q", backlog)
	}
}

func TestNilBrokerDropsEvents(t *testing.T) {
	var b *Broker
	b.Publish(Event{Type: TypeMaster})
}

// An open stream must not hold a shutting-down server to its timeout.
func TestShutdownEndsStreams(t *testing.T) {
	b := NewBroker()
	srv := httptest.NewUnstartedServer(b)
	srv.Config.RegisterOnShutdown(b.Close)
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with an open stream: %v after %s", err, time.Since(start))
	}

	// A stream opened after Close ends at once.
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		b.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream opened after Close didn't end")
	}
	b.Close() // twice is fine
}
