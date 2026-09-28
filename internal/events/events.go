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
	TypeStatus  = "status"  // what the stacker is doing; not replayed
)

// Status is a snapshot of the stacker's work.
type Status struct {
	Type    string    `json:"type"`
	Workers []Worker  `json:"workers"`
	Backlog Backlog   `json:"backlog"`
	Time    time.Time `json:"time"`
}

// Worker is one target being stacked: its stage, and how far through it.
type Worker struct {
	Object  string    `json:"object"`
	Filter  string    `json:"filter"`
	Stage   string    `json:"stage"`
	Done    int       `json:"done"`
	Total   int       `json:"total"`
	Started time.Time `json:"started"`
}

// Backlog counts the work left.
type Backlog struct {
	// Lights waiting to be stacked, and those already decided (added,
	// or left out for a reason).
	LightsPending int `json:"lights_pending"`
	LightsDone    int `json:"lights_done"`
	Dead          int `json:"dead"`
	// Previews still to render.
	PreviewsPending int `json:"previews_pending"`
}

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
	subs   map[chan []byte]struct{}
	status []byte // latest Status, sent to new listeners first
}

func NewBroker() *Broker {
	// IDs start from the clock so they keep rising across restarts, and a
	// listener's Last-Event-ID from before a restart replays nothing wrong.
	return &Broker{next: uint64(time.Now().UnixMilli()), subs: map[chan []byte]struct{}{}}
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
	b.send(frame(e))
}

// SetStatus sends a status snapshot to listeners and keeps it for new ones.
func (b *Broker) SetStatus(s Status) {
	if b == nil {
		return
	}
	s.Type = TypeStatus
	if s.Time.IsZero() {
		s.Time = time.Now()
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("data: %s\n\n", data))
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = msg
	b.send(msg)
}

// send must be called with b.mu held.
func (b *Broker) send(msg []byte) {
	for ch := range b.subs {
		select {
		case ch <- msg:
		default:
			// A listener this far behind refetches when it catches up.
		}
	}
}

// frame formats an event as an SSE message with its ID.
func frame(e Event) []byte {
	data, _ := json.Marshal(e)
	return []byte(fmt.Sprintf("id: %d\ndata: %s\n\n", e.ID, data))
}

// Subscribe returns the messages a listener that saw event after has
// missed, starting with the latest status, and a channel of new ones until
// cancel is called.
func (b *Broker) Subscribe(after uint64) (backlog [][]byte, ch <-chan []byte, cancel func()) {
	c := make(chan []byte, 64)
	b.mu.Lock()
	if b.status != nil {
		backlog = append(backlog, b.status)
	}
	for _, e := range b.recent {
		if e.ID > after {
			backlog = append(backlog, frame(e))
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

// Stream writes SSE messages to w until the request ends. The server's
// write timeout is lifted for the stream.
func Stream(w http.ResponseWriter, r *http.Request, backlog [][]byte, ch <-chan []byte) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(msg []byte) error {
		if _, err := w.Write(msg); err != nil {
			return err
		}
		return rc.Flush()
	}
	// Tell the browser to retry a dropped stream after 3 s.
	if write([]byte("retry: 3000\n\n")) != nil {
		return
	}
	for _, msg := range backlog {
		if write(msg) != nil {
			return
		}
	}
	tick := time.NewTicker(Heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if write(msg) != nil {
				return
			}
		case <-tick.C:
			if write([]byte(": keepalive\n\n")) != nil {
				return
			}
		}
	}
}
