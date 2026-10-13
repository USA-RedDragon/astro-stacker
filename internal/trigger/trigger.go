// Package trigger runs work as soon as its input changes: a burst of kicks
// becomes one run once the kicks go quiet, with a slow backstop for changes
// no kick reports.
package trigger

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Debouncer collects kicks and runs work once they settle. Runs never
// overlap: kicks that come during a run are collected for the next one.
// The zero value runs each kick at once.
type Debouncer struct {
	// Quiet is how long after the last kick a run starts.
	Quiet time.Duration
	// MaxWait, when above zero, starts a run no later than this after the
	// first kick of a burst, so a steady stream of kicks still runs.
	MaxWait time.Duration
	// StartAfter delays the first run after Run starts; kicks before then
	// wait for it.
	StartAfter time.Duration

	mu        sync.Mutex
	keys      map[string]struct{}
	all       bool
	first     time.Time
	last      time.Time
	notBefore time.Time
	starting  bool
	kick      chan struct{}
}

func (d *Debouncer) signal() chan struct{} {
	if d.kick == nil {
		d.kick = make(chan struct{}, 1)
	}
	return d.kick
}

// Kick asks for a run covering keys; no keys asks for a run covering
// everything. A nil Debouncer ignores kicks.
func (d *Debouncer) Kick(keys ...string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	now := time.Now()
	if d.first.IsZero() {
		d.first = now
	}
	d.last = now
	if len(keys) == 0 {
		d.all = true
	}
	for _, k := range keys {
		if d.keys == nil {
			d.keys = map[string]struct{}{}
		}
		d.keys[k] = struct{}{}
	}
	ch := d.signal()
	d.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// due reports when the pending burst should run, false with none pending.
func (d *Debouncer) due() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.starting {
		return d.notBefore, true
	}
	if d.first.IsZero() {
		return time.Time{}, false
	}
	at := d.last.Add(d.Quiet)
	if d.MaxWait > 0 {
		if capAt := d.first.Add(d.MaxWait); capAt.Before(at) {
			at = capAt
		}
	}
	if at.Before(d.notBefore) {
		at = d.notBefore
	}
	return at, true
}

// take empties the pending burst and returns its keys, nil when it covers
// everything.
func (d *Debouncer) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	if !d.all {
		out = make([]string, 0, len(d.keys))
		for k := range d.keys {
			out = append(out, k)
		}
		slices.Sort(out)
	}
	d.keys, d.all, d.first, d.last, d.starting = nil, false, time.Time{}, time.Time{}, false
	return out
}

// Run calls run first after StartAfter and then for each settled burst of
// kicks, and also after backstop with no run in between when backstop is
// above zero, until ctx ends. run gets the burst's keys, or nil for a run
// that covers everything: the first, a backstop, or one kicked without
// keys.
func (d *Debouncer) Run(ctx context.Context, backstop time.Duration, run func(ctx context.Context, keys []string)) {
	d.mu.Lock()
	kick := d.signal()
	now := time.Now()
	d.notBefore = now.Add(d.StartAfter)
	d.all, d.starting = true, true
	d.mu.Unlock()
	var back <-chan time.Time
	var backTimer *time.Timer
	if backstop > 0 {
		backTimer = time.NewTimer(backstop)
		defer backTimer.Stop()
		back = backTimer.C
	}
	settle := time.NewTimer(0)
	settle.Stop()
	defer settle.Stop()
	for {
		var settled <-chan time.Time
		if at, ok := d.due(); ok {
			settle.Reset(max(0, time.Until(at)))
			settled = settle.C
		}
		select {
		case <-ctx.Done():
			return
		case <-kick:
			settle.Stop()
			continue
		case <-back:
			settle.Stop()
			d.take()
			run(ctx, nil)
		case <-settled:
			if at, ok := d.due(); !ok || time.Now().Before(at) {
				continue
			}
			run(ctx, d.take())
		}
		if ctx.Err() != nil {
			return
		}
		if backTimer != nil {
			backTimer.Reset(backstop)
		}
	}
}
