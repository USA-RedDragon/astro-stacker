package trigger_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/trigger"
)

const (
	m31 = "M31"
	m33 = "M33"
)

type recorder struct {
	mu   sync.Mutex
	runs [][]string
	at   []time.Time
	ch   chan struct{}
}

func newRecorder() *recorder { return &recorder{ch: make(chan struct{}, 100)} }

func (r *recorder) run(_ context.Context, keys []string) {
	r.mu.Lock()
	r.runs = append(r.runs, keys)
	r.at = append(r.at, time.Now())
	r.mu.Unlock()
	r.ch <- struct{}{}
}

func (r *recorder) wait(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for range n {
		select {
		case <-r.ch:
		case <-deadline:
			t.Fatalf("waited %v for %d runs; got %d", within, n, len(r.snapshot()))
		}
	}
}

func (r *recorder) snapshot() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.runs)
}

func start(t *testing.T, d *trigger.Debouncer, backstop time.Duration, run func(context.Context, []string)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx, backstop, run)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestFirstRunCoversEverything(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	start(t, &trigger.Debouncer{Quiet: time.Hour}, 0, r.run)
	r.wait(t, 1, time.Second)
	if got := r.snapshot(); len(got) != 1 || got[0] != nil {
		t.Fatalf("runs %v", got)
	}
}

func TestBurstRunsOnceWithEveryKey(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: 50 * time.Millisecond}
	start(t, d, 0, r.run)
	r.wait(t, 1, time.Second)
	for i := range 50 {
		d.Kick([]string{m31, m33, "M42"}[i%3])
	}
	r.wait(t, 1, 2*time.Second)
	time.Sleep(150 * time.Millisecond)
	got := r.snapshot()
	if len(got) != 2 || !slices.Equal(got[1], []string{m31, m33, "M42"}) {
		t.Fatalf("a burst of 50 kicks runs once with its keys: %v", got)
	}
}

func TestKickWithoutKeysCoversEverything(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: 10 * time.Millisecond}
	start(t, d, 0, r.run)
	r.wait(t, 1, time.Second)
	d.Kick(m31)
	d.Kick()
	r.wait(t, 1, time.Second)
	if got := r.snapshot(); got[1] != nil {
		t.Fatalf("runs %v", got)
	}
}

func TestQuietWaitsForTheBurstToEnd(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: 80 * time.Millisecond}
	start(t, d, 0, r.run)
	r.wait(t, 1, time.Second)
	began := time.Now()
	for range 5 {
		d.Kick(m31)
		time.Sleep(30 * time.Millisecond)
	}
	r.wait(t, 1, 2*time.Second)
	if waited := time.Since(began); waited < 200*time.Millisecond {
		t.Fatalf("ran %v after the first kick, before the burst went quiet", waited)
	}
}

func TestMaxWaitRunsDuringASteadyStream(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: time.Hour, MaxWait: 60 * time.Millisecond}
	start(t, d, 0, r.run)
	r.wait(t, 1, time.Second)
	stop := time.After(400 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
			d.Kick(m31)
			time.Sleep(5 * time.Millisecond)
		}
	}
	if got := r.snapshot(); len(got) < 3 {
		t.Fatalf("a steady stream ran only %d times in 400ms with a 60ms cap", len(got)-1)
	}
}

func TestBackstopRunsWithoutKicks(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	start(t, &trigger.Debouncer{Quiet: time.Hour}, 30*time.Millisecond, r.run)
	r.wait(t, 3, 2*time.Second)
	for _, keys := range r.snapshot() {
		if keys != nil {
			t.Fatalf("a backstop run covers everything: %v", keys)
		}
	}
}

func TestStartAfterHoldsKicks(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{StartAfter: 150 * time.Millisecond}
	began := time.Now()
	start(t, d, 0, r.run)
	time.Sleep(20 * time.Millisecond)
	d.Kick(m31)
	r.wait(t, 1, 2*time.Second)
	if waited := time.Since(began); waited < 150*time.Millisecond {
		t.Fatalf("first run after %v, before StartAfter", waited)
	}
	if got := r.snapshot(); len(got) != 1 || got[0] != nil {
		t.Fatalf("the kick joins the first run: %v", got)
	}
}

func TestKicksDuringARunRunAgainWithoutOverlap(t *testing.T) {
	t.Parallel()
	var running, overlap atomic.Int32
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: 5 * time.Millisecond}
	entered, release := make(chan struct{}), make(chan struct{})
	first := true
	start(t, d, 0, func(ctx context.Context, keys []string) {
		if running.Add(1) > 1 {
			overlap.Add(1)
		}
		if first {
			first = false
			close(entered)
			<-release
		}
		running.Add(-1)
		r.run(ctx, keys)
	})
	<-entered
	d.Kick(m31)
	d.Kick(m33)
	close(release)
	r.wait(t, 2, time.Second)
	got := r.snapshot()
	if overlap.Load() != 0 || len(got) != 2 || !slices.Equal(got[1], []string{m31, m33}) {
		t.Fatalf("overlap %d, runs %v", overlap.Load(), got)
	}
}

func TestNilDebouncerIgnoresKicks(t *testing.T) {
	t.Parallel()
	var d *trigger.Debouncer
	d.Kick(m31)
}

func TestMinGapSpacesRunsDuringASteadyStream(t *testing.T) {
	t.Parallel()
	r := newRecorder()
	d := &trigger.Debouncer{Quiet: 5 * time.Millisecond, MinGap: 150 * time.Millisecond}
	start(t, d, 0, r.run)
	r.wait(t, 1, time.Second)
	stop := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-stop:
			break loop
		default:
			d.Kick(m31)
			time.Sleep(20 * time.Millisecond)
		}
	}
	r.wait(t, 1, time.Second)
	got := r.snapshot()
	if len(got) > 5 {
		t.Fatalf("kicks 20ms apart ran %d times in 500ms with a 150ms gap", len(got))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 2; i < len(r.at); i++ {
		if gap := r.at[i].Sub(r.at[i-1]); gap < 140*time.Millisecond {
			t.Fatalf("runs %d and %d were %v apart", i-1, i, gap)
		}
	}
}
