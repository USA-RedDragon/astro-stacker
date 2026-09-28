// Package events tells listeners when previews and masters change, as a
// server-sent event stream.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	TypePreview = "preview" // a sub's preview was rendered
	TypeMaster  = "master"  // a master was updated
)

type Event struct {
	ID     uint64    `json:"id"`
	Type   string    `json:"type"`
	Object string    `json:"object"`
	Filter string    `json:"filter,omitempty"`
	Key    string    `json:"key,omitempty"`
	Time   time.Time `json:"time"`
}

// keep is how many recent events a reconnecting listener can catch up on.
const keep = 512

// Broker fans events out to subscribers. A nil Broker drops events.
type Broker struct {
	mu     sync.Mutex
	next   uint64
	recent []Event
	subs   map[chan Event]struct{}
}

func NewBroker() *Broker {
	// IDs start from the clock so they keep rising across restarts, and a
	// listener's Last-Event-ID from before a restart replays nothing wrong.
	return &Broker{next: uint64(time.Now().UnixMilli()), subs: map[chan Event]struct{}{}}
}

func (b *Broker) Publish(e Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	e.ID = b.next
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.recent = append(b.recent, e)
	if len(b.recent) > keep {
		b.recent = b.recent[len(b.recent)-keep:]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			// A listener this far behind refetches when it catches up.
		}
	}
}

// Subscribe returns the events after id still held, and a channel of new
// ones until cancel is called.
func (b *Broker) Subscribe(after uint64) (backlog []Event, ch <-chan Event, cancel func()) {
	c := make(chan Event, 64)
	b.mu.Lock()
	for _, e := range b.recent {
		if e.ID > after {
			backlog = append(backlog, e)
		}
	}
	b.subs[c] = struct{}{}
	b.mu.Unlock()
	return backlog, c, func() {
		b.mu.Lock()
		delete(b.subs, c)
		b.mu.Unlock()
	}
}

// Heartbeat keeps idle streams open through proxies.
const Heartbeat = 15 * time.Second

// ServeHTTP streams events as server-sent events, starting after the
// Last-Event-ID header when a listener reconnects.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	backlog, ch, cancel := b.Subscribe(after)
	defer cancel()
	Stream(w, r, backlog, ch)
}

// Stream writes events to w until the request ends. The server's write
// timeout is lifted for the stream.
func Stream(w http.ResponseWriter, r *http.Request, backlog []Event, ch <-chan Event) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(e Event) error {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.ID, data); err != nil {
			return err
		}
		return rc.Flush()
	}
	// Tell the browser to retry a dropped stream after 3 s.
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	for _, e := range backlog {
		if write(e) != nil {
			return
		}
	}
	if rc.Flush() != nil {
		return
	}
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if write(e) != nil {
				return
			}
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
