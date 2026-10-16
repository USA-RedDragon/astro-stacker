// Package activity collects what every long-running background process of
// the astro pipeline is doing, in plain words with real progress, for the
// processing indicator.
package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"
)

const (
	StateRunning = "running"
	StateQueued  = "queued"
	StateIdle    = "idle"
	StateFailed  = "failed"
)

// Item is one kind of background work. Done of Total in Unit is the
// progress of what runs now; Total 0 means nothing measurable.
type Item struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Label        string     `json:"label"`
	State        string     `json:"state"`
	Done         int64      `json:"done"`
	Total        int64      `json:"total"`
	Unit         string     `json:"unit,omitempty"`
	Detail       string     `json:"detail,omitempty"`
	Current      string     `json:"current,omitempty"`
	Queued       int        `json:"queued"`
	QueueUnit    string     `json:"queue_unit,omitempty"`
	Started      *time.Time `json:"started,omitempty"`
	NextRun      *time.Time `json:"next_run,omitempty"`
	LastFinished *time.Time `json:"last_finished,omitempty"`
	LastFailed   *time.Time `json:"last_failed,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Warning      string     `json:"warning,omitempty"`
	Order        int        `json:"-"`
}

// Snapshot is every item, busy ones first.
type Snapshot struct {
	Items []Item    `json:"items"`
	Time  time.Time `json:"time"`
}

// Source reads items from outside this process, such as Kubernetes Jobs
// or rows other processes write.
type Source struct {
	Name  string
	Every time.Duration
	Read  func(ctx context.Context) ([]Item, error)
}

// Broadcaster sends a snapshot to listeners and keeps it for new ones.
type Broadcaster interface {
	Broadcast(typ string, v any, sticky bool)
}

// Resend is how often an unchanged snapshot is sent again, so listeners can
// tell a current one from one left over by a stacker that stopped.
const Resend = time.Minute

// EventType is the event the snapshot is sent as.
const EventType = "activity"

// Registry holds this process's tasks and the outside sources.
type Registry struct {
	Now func() time.Time

	mu      sync.Mutex
	tasks   map[string]*Task
	order   int
	sources []*sourceState
	merge   []func([]Item) []Item
	changed chan struct{}
}

type sourceState struct {
	Source
	mu    sync.Mutex
	items []Item
	err   error
	read  time.Time
}

func NewRegistry() *Registry {
	return &Registry{tasks: map[string]*Task{}, changed: make(chan struct{}, 1)}
}

func (r *Registry) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Task returns the task for kind, made idle with label the first time.
// A nil Registry returns a nil Task, whose methods do nothing.
func (r *Registry) Task(kind, label string) *Task {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[kind]; ok {
		return t
	}
	t := &Task{r: r, item: Item{Kind: kind, Label: label, State: StateIdle, Order: r.order}}
	r.order++
	r.tasks[kind] = t
	return t
}

// AddSource reads s every s.Every while Run runs.
func (r *Registry) AddSource(s Source) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, &sourceState{Source: s})
}

// AddMerge lets fn rewrite the items before they are sent, as when an
// outside source and a task describe the same work.
func (r *Registry) AddMerge(fn func([]Item) []Item) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.merge = append(r.merge, fn)
}

func (r *Registry) notify() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// Snapshot is every task and source item now.
func (r *Registry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{Items: []Item{}, Time: time.Now().UTC()}
	}
	r.mu.Lock()
	tasks := make([]*Task, 0, len(r.tasks))
	for _, t := range r.tasks {
		tasks = append(tasks, t)
	}
	sources := append([]*sourceState(nil), r.sources...)
	merge := append([]func([]Item) []Item(nil), r.merge...)
	r.mu.Unlock()
	items := make([]Item, 0, len(tasks))
	for _, t := range tasks {
		items = append(items, t.Item())
	}
	for i, s := range sources {
		s.mu.Lock()
		for _, it := range s.items {
			it.Order += 1000 * (i + 1)
			items = append(items, it)
		}
		s.mu.Unlock()
	}
	for _, fn := range merge {
		items = fn(items)
	}
	for i := range items {
		if items[i].ID == "" {
			items[i].ID = items[i].Kind
		}
	}
	Sort(items)
	return Snapshot{Items: items, Time: r.now()}
}

// Sort puts failed and running items first, then queued, then idle,
// keeping the registered order within each.
func Sort(items []Item) {
	rank := map[string]int{StateFailed: 0, StateRunning: 1, StateQueued: 2, StateIdle: 3}
	sort.SliceStable(items, func(a, b int) bool {
		ra, rb := rank[items[a].State], rank[items[b].State]
		if ra != rb {
			return ra < rb
		}
		return items[a].Order < items[b].Order
	})
}

// Run reads the sources on their intervals and sends a snapshot whenever it
// changes, until ctx ends.
func (r *Registry) Run(ctx context.Context, out Broadcaster) {
	if r == nil {
		return
	}
	r.mu.Lock()
	sources := append([]*sourceState(nil), r.sources...)
	r.mu.Unlock()
	for _, s := range sources {
		go r.poll(ctx, s)
	}
	var last []byte
	var sent time.Time
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		snap := r.Snapshot()
		key, err := json.Marshal(snap.Items)
		if err == nil && (!bytes.Equal(key, last) || time.Since(sent) >= Resend) {
			last, sent = key, time.Now()
			out.Broadcast(EventType, snap, true)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-r.changed:
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
}

func (r *Registry) poll(ctx context.Context, s *sourceState) {
	every := s.Every
	if every <= 0 {
		every = 30 * time.Second
	}
	for {
		rctx, cancel := context.WithTimeout(ctx, max(every, 20*time.Second))
		items, err := s.Read(rctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if err != nil {
			if s.err == nil || s.err.Error() != err.Error() {
				slog.Warn("Reading background activity failed", "source", s.Name, "error", err)
			}
			if r.now().Sub(s.read) > 3*every {
				s.items = nil
			}
		} else {
			s.items = items
			s.read = r.now()
		}
		s.err = err
		s.mu.Unlock()
		r.notify()
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// Task is one kind of work this process does.
type Task struct {
	r    *Registry
	mu   sync.Mutex
	item Item
}

// Item is the task's state now.
func (t *Task) Item() Item {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.item
}

func (t *Task) update(fn func(*Item)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	fn(&t.item)
	t.mu.Unlock()
	t.r.notify()
}

// Start marks a run begun on current, with total of unit to do.
func (t *Task) Start(current string, total int64, unit string) {
	t.update(func(it *Item) {
		now := t.r.now()
		it.State, it.Current, it.Done, it.Total, it.Unit, it.Started = StateRunning, current, 0, max(total, 0), unit, &now
	})
}

// SetTotal changes how much the run has to do.
func (t *Task) SetTotal(total int64) {
	t.update(func(it *Item) { it.Total = max(total, 0) })
}

// SetProgress sets done of total.
func (t *Task) SetProgress(done, total int64) {
	t.update(func(it *Item) { it.Done, it.Total = max(done, 0), max(total, 0) })
}

// Step adds n to done.
func (t *Task) Step(n int64) {
	t.update(func(it *Item) { it.Done += n })
}

// SetCurrent names what the run works on now.
func (t *Task) SetCurrent(current string) {
	t.update(func(it *Item) { it.Current = current })
}

// SetQueued says how much waits after the run.
func (t *Task) SetQueued(n int, unit string) {
	t.update(func(it *Item) { it.Queued, it.QueueUnit = max(n, 0), unit })
}

// SetNext says when the next run starts by itself.
func (t *Task) SetNext(at time.Time) {
	t.update(func(it *Item) {
		if at.IsZero() {
			it.NextRun = nil
			return
		}
		at = at.UTC()
		it.NextRun = &at
	})
}

// SetLabel renames the task.
func (t *Task) SetLabel(label string) {
	t.update(func(it *Item) { it.Label = label })
}

// Finish ends the run; err marks it failed until the next run starts.
func (t *Task) Finish(err error) {
	t.update(func(it *Item) {
		now := t.r.now()
		it.Current, it.Started = "", nil
		if err != nil {
			it.State, it.LastFailed, it.LastError = StateFailed, &now, err.Error()
			return
		}
		it.State, it.LastFinished, it.Done, it.Total = StateIdle, &now, 0, 0
		it.LastError = ""
	})
}

// Running says whether a run is under way.
func (t *Task) Running() bool {
	if t == nil {
		return false
	}
	return t.Item().State == StateRunning
}

// The kinds of background work.
const (
	KindStacking     = "stacking"
	KindPreviews     = "previews"
	KindIndexer      = "indexer"
	KindPhotometry   = "photometry"
	KindDust         = "dust"
	KindGoals        = "goals"
	KindDiscoveries  = "discoveries"
	KindVetting      = "vetting"
	KindTESS         = "tess"
	KindTNS          = "tns"
	KindMorning      = "morning-report"
	KindSkyEvents    = "sky-events"
	KindBodies       = "bodies"
	KindBodyNights   = "body-nights"
	KindStarfront    = "starfront"
	KindSmallBodies  = "small-bodies"
	KindMirrorPrefix = "catalog-mirror-"
	KindCalibration  = "calibration"
)
