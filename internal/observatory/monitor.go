package observatory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/schedcmd"
	"github.com/coder/websocket"
)

const (
	PollInterval   = 15 * time.Second
	minBackoff     = time.Second
	maxBackoff     = 30 * time.Second
	eventScheduler = "scheduler"
)

type Broadcaster interface {
	Broadcast(typ string, v any, sticky bool)
}

type ResultSink interface {
	ApplyResult(ctx context.Context, res schedcmd.Result) (schedcmd.Record, error)
}

type Monitor struct {
	Client       *Client
	Results      ResultSink
	Broker       Broadcaster
	OnOnline     func()
	PollInterval time.Duration
	Now          func() time.Time

	mu         sync.Mutex
	status     *Status
	reachable  Reachability
	lastAnswer time.Time
	since      time.Time
	live       bool
	lastErr    string
}

func NewMonitor(c *Client, results ResultSink, broker Broadcaster) *Monitor {
	return &Monitor{Client: c, Results: results, Broker: broker}
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Monitor) Status() any {
	return m.View()
}

func (m *Monitor) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked()
}

func (m *Monitor) viewLocked() View {
	v := View{Reachable: m.reachable, Live: m.live, Error: m.lastErr}
	if v.Reachable == "" {
		v.Reachable = Offline
		if !m.Client.Configured() {
			v.Reachable = Unconfigured
		}
	}
	if m.status != nil {
		s := *m.status
		v.Status = &s
	}
	if !m.lastAnswer.IsZero() {
		t := m.lastAnswer
		v.LastAnswer = &t
	}
	if !m.since.IsZero() {
		t := m.since
		v.Since = &t
	}
	return v
}

func (m *Monitor) publish() {
	if m.Broker == nil {
		return
	}
	m.Broker.Broadcast(eventScheduler, m.View(), true)
}

func (m *Monitor) setReachable(r Reachability, live bool, errText string) bool {
	m.mu.Lock()
	changed := m.reachable != r
	becameOnline := changed && r == Online
	if changed {
		m.reachable = r
		m.since = m.now()
	}
	if r != Online {
		live = false
	}
	changed = changed || m.live != live || m.lastErr != errText
	m.live = live
	m.lastErr = errText
	m.mu.Unlock()
	if becameOnline && m.OnOnline != nil {
		go m.OnOnline()
	}
	return changed
}

func (m *Monitor) SetStatus(s Status, live bool) {
	m.mu.Lock()
	m.status = &s
	m.lastAnswer = m.now()
	m.mu.Unlock()
	m.setReachable(Online, live, "")
	m.publish()
}

func (m *Monitor) MarkDown(err error) {
	r := Offline
	if !m.Client.Configured() {
		r = Unconfigured
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	if m.setReachable(r, false, text) {
		m.publish()
	}
}

func (m *Monitor) isLive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live
}

func (m *Monitor) Run(ctx context.Context) {
	if !m.Client.Configured() {
		m.MarkDown(nil)
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() { m.pollLoop(ctx) })
	m.socketLoop(ctx)
	wg.Wait()
}

func (m *Monitor) Poll(ctx context.Context) error {
	s, err := m.Client.Status(ctx)
	if err != nil {
		m.MarkDown(err)
		return err
	}
	m.SetStatus(s, m.isLive())
	return nil
}

func (m *Monitor) pollLoop(ctx context.Context) {
	every := m.PollInterval
	if every <= 0 {
		every = PollInterval
	}
	if !m.isLive() {
		_ = m.Poll(ctx)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !m.isLive() {
				_ = m.Poll(ctx)
			}
		}
	}
}

func (m *Monitor) socketLoop(ctx context.Context) {
	backoff := minBackoff
	for ctx.Err() == nil {
		start := m.now()
		err := m.Listen(ctx)
		if ctx.Err() != nil {
			return
		}
		if m.now().Sub(start) > maxBackoff {
			backoff = minBackoff
		}
		m.mu.Lock()
		m.live = false
		m.mu.Unlock()
		if err != nil && !errors.Is(err, schedcmd.ErrUnreachable) {
			slog.Warn("Scheduler WebSocket failed", "error", err)
		}
		_ = m.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (m *Monitor) Listen(ctx context.Context) error {
	conn, err := m.Client.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	m.mu.Lock()
	m.live = true
	m.mu.Unlock()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return unreachable(err)
		}
		if typ != websocket.MessageText {
			continue
		}
		m.handle(ctx, data)
	}
}

func (m *Monitor) handle(ctx context.Context, data []byte) {
	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Debug("Ignoring a scheduler frame that is not JSON", "error", err)
		return
	}
	switch f.Type {
	case "status":
		var s Status
		if err := json.Unmarshal(f.Data, &s); err != nil {
			slog.Warn("Ignoring a scheduler status that does not parse", "error", err)
			return
		}
		m.SetStatus(s, true)
	case "result":
		var r schedcmd.Result
		if err := json.Unmarshal(f.Data, &r); err != nil || r.ID == "" {
			slog.Warn("Ignoring a scheduler result that does not parse", "error", err)
			return
		}
		m.applyResult(ctx, r)
	}
}

func (m *Monitor) applyResult(ctx context.Context, r schedcmd.Result) {
	if m.Results == nil {
		return
	}
	if _, err := m.Results.ApplyResult(ctx, r); err != nil && !errors.Is(err, schedcmd.ErrNotFound) {
		slog.Warn("Recording a scheduler result failed", "id", r.ID, "error", err)
	}
}

func (m *Monitor) SchedulerState() (schedcmd.SchedulerState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reachable != Online || m.status == nil {
		return schedcmd.SchedulerState{}, false
	}
	s := m.status
	out := schedcmd.SchedulerState{State: s.State, Paused: s.Paused, PauseRequested: s.PauseRequested}
	for _, k := range s.Skips {
		out.Skips = append(out.Skips, schedcmd.SkipState{Scope: k.Scope, TargetID: k.TargetID, ProjectID: k.ProjectID, Until: k.Until})
	}
	return out, true
}
