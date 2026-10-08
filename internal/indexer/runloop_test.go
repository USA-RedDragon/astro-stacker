package indexer

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recorder logs the loop's calls in order.
type recorder struct {
	mu      sync.Mutex
	calls   []string
	backlog int // passes that still report a backlog
}

func (r *recorder) scan(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "scan")
}

func (r *recorder) pass(context.Context) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "pass")
	time.Sleep(5 * time.Millisecond) // a batch takes time
	if r.backlog > 0 {
		r.backlog--
		return true
	}
	return false
}

func (r *recorder) count(what string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == what {
			n++
		}
	}
	return n
}

func TestRunLoopDrainsBacklogBackToBack(t *testing.T) {
	t.Parallel()
	r := &recorder{backlog: 20}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Hour, r.scan, r.pass)
		close(done)
	}()
	// 20 backlog passes, then one that finds none and waits out the hour.
	deadline := time.Now().Add(5 * time.Second)
	for r.count("pass") < 21 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if got := r.count("pass"); got != 21 {
		t.Errorf("passes = %d, want 21: back to back while a backlog remains, then wait", got)
	}
	if got := r.count("scan"); got != 1 {
		t.Errorf("scans = %d, want 1 within the interval", got)
	}
}

func TestRunLoopScansOnTimeDuringBacklog(t *testing.T) {
	t.Parallel()
	r := &recorder{backlog: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, 30*time.Millisecond, r.scan, r.pass)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	// A scan every interval, each at most one batch late.
	if got := r.count("scan"); got < 4 {
		t.Errorf("scans = %d in 200 ms at a 30 ms interval, want at least 4", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run := 0
	for _, c := range r.calls {
		if c == "pass" {
			run++
			if run > 8 {
				t.Fatalf("%d passes in a row without a scan: %v", run, r.calls)
			}
		} else {
			run = 0
		}
	}
}

func TestRunLoopWaitsWithoutBacklog(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Hour, r.scan, r.pass)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if s, p := r.count("scan"), r.count("pass"); s != 1 || p != 1 {
		t.Errorf("scans %d passes %d, want 1 and 1", s, p)
	}
}
