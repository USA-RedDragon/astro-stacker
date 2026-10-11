package stacking

import (
	"context"
	"testing"
	"time"
)

func TestPauseUntilWakes(t *testing.T) {
	t.Parallel()
	p := &Pipeline{drain: make(chan struct{})}
	woke := make(chan struct{})
	close(woke)
	start := time.Now()
	if !p.pauseUntil(context.Background(), time.Hour, woke) {
		t.Fatal("pause ended as if stopping")
	}
	if time.Since(start) > time.Second {
		t.Fatal("pause did not end on wake")
	}
}
