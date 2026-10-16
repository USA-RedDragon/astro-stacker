package activity

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Report is the progress a batch Job writes where the stacker can read it.
type Report struct {
	State   string    `json:"state"`
	Current string    `json:"current,omitempty"`
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Unit    string    `json:"unit,omitempty"`
	Queued  int       `json:"queued"`
	QUnit   string    `json:"queue_unit,omitempty"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
	Error   string    `json:"error,omitempty"`
}

const (
	ReportRunning = "running"
	ReportDone    = "done"
	ReportFailed  = "failed"
)

// StaleAfter is how long a running report may go unwritten before it is
// taken to be from a Job that died.
const StaleAfter = 3 * time.Minute

// ParseReport reads a report.
func ParseReport(b []byte) (Report, error) {
	var r Report
	err := json.Unmarshal(b, &r)
	return r, err
}

// Live says whether the report is from a run still going at now.
func (r Report) Live(now time.Time) bool {
	return r.State == ReportRunning && now.Sub(r.Updated) < StaleAfter
}

// Reporter writes a Job's progress with Put at most every Every, and on
// Finish.
type Reporter struct {
	Put   func(ctx context.Context, data []byte) error
	Every time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	rep     Report
	counter *atomic.Int64
	dirty   bool
	stop    chan struct{}
	stopped sync.Once
	wg      sync.WaitGroup
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Start begins a run and writes the report every Every until Finish.
// A nil Reporter does nothing.
func (r *Reporter) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	now := r.now()
	r.rep = Report{State: ReportRunning, Started: now, Updated: now}
	r.stop = make(chan struct{})
	r.mu.Unlock()
	r.flush(ctx, true)
	every := r.Every
	if every <= 0 {
		every = 10 * time.Second
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				r.flush(ctx, true)
			}
		}
	}()
}

// Set records the step and its progress.
func (r *Reporter) Set(current string, done, total int64, unit string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.rep.Current, r.rep.Done, r.rep.Total, r.rep.Unit = current, done, total, unit
	r.counter = nil
	r.dirty = true
	r.mu.Unlock()
}

// Track records the step, reading done from counter at each write.
func (r *Reporter) Track(current, unit string, total int64, counter *atomic.Int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.rep.Current, r.rep.Done, r.rep.Total, r.rep.Unit = current, 0, total, unit
	r.counter = counter
	r.dirty = true
	r.mu.Unlock()
}

// Queue records how much waits after the current step.
func (r *Reporter) Queue(n int, unit string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.rep.Queued, r.rep.QUnit = n, unit
	r.dirty = true
	r.mu.Unlock()
}

// Progress records done of total within the current step.
func (r *Reporter) Progress(done, total int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.rep.Done, r.rep.Total = done, total
	r.counter = nil
	r.dirty = true
	r.mu.Unlock()
}

// flush writes the report; heartbeat writes it even when unchanged, so
// readers can tell a live run from a dead one.
func (r *Reporter) flush(ctx context.Context, heartbeat bool) {
	r.mu.Lock()
	if !r.dirty && !heartbeat {
		r.mu.Unlock()
		return
	}
	if r.counter != nil {
		r.rep.Done = r.counter.Load()
	}
	r.rep.Updated = r.now()
	r.dirty = false
	b, err := json.Marshal(r.rep)
	r.mu.Unlock()
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := r.Put(wctx, b); err != nil {
		slog.Warn("Could not write the progress report", "error", err)
	}
}

// Finish ends the run as done, or failed with err.
func (r *Reporter) Finish(ctx context.Context, err error) {
	if r == nil {
		return
	}
	r.stopped.Do(func() {
		if r.stop != nil {
			close(r.stop)
		}
	})
	r.wg.Wait()
	r.mu.Lock()
	r.rep.State = ReportDone
	if err != nil {
		r.rep.State, r.rep.Error = ReportFailed, err.Error()
	}
	r.mu.Unlock()
	r.flush(ctx, true)
}

// FromReport fills an item from a report the Job wrote, when the report is
// from a live run.
func (it *Item) FromReport(r Report, now time.Time) bool {
	if !r.Live(now) {
		return false
	}
	it.State = StateRunning
	it.Current, it.Done, it.Total, it.Unit = r.Current, r.Done, r.Total, r.Unit
	it.Queued, it.QueueUnit = r.Queued, r.QUnit
	s := r.Started
	it.Started = &s
	return true
}
