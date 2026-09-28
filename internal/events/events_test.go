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
}

func TestNilBrokerDropsEvents(t *testing.T) {
	var b *Broker
	b.Publish(Event{Type: TypeMaster})
}
