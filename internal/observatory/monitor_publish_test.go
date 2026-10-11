package observatory_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/USA-RedDragon/astro-stacker/internal/observatory"
)

type stallingBroker struct {
	mu      sync.Mutex
	views   []observatory.View
	stalled chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *stallingBroker) Broadcast(_ string, v any, _ bool) {
	first := false
	b.once.Do(func() { first = true })
	if first {
		close(b.stalled)
		<-b.release
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if view, ok := v.(observatory.View); ok {
		b.views = append(b.views, view)
	}
}

func (b *stallingBroker) snapshot() []observatory.View {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]observatory.View(nil), b.views...)
}

func TestStickyViewFollowsTheLastChange(t *testing.T) {
	t.Parallel()
	b := &stallingBroker{stalled: make(chan struct{}), release: make(chan struct{})}
	m := observatory.NewMonitor(observatory.NewClient("http://observatory.invalid", ""), nil, b)
	var wg sync.WaitGroup
	wg.Go(func() { m.SetStatus(observatory.Status{State: stateImaging}, true) })
	<-b.stalled
	wg.Go(func() { m.MarkDown(errors.New("connection refused")) })
	deadline := time.Now().Add(5 * time.Second)
	for m.View().Reachable != observatory.Offline && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	settle := time.Now().Add(time.Second)
	for len(b.snapshot()) == 0 && time.Now().Before(settle) {
		time.Sleep(time.Millisecond)
	}
	close(b.release)
	wg.Wait()
	views := b.snapshot()
	if len(views) == 0 {
		t.Fatal("nothing broadcast")
	}
	last := views[len(views)-1]
	if want := m.View().Reachable; last.Reachable != want {
		t.Fatalf("listeners last heard %s, the monitor says %s", last.Reachable, want)
	}
}
