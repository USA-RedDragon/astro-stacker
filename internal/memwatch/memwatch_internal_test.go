package memwatch

import (
	"testing"
	"time"
)

func TestStepAndPeak(t *testing.T) {
	t.Parallel()
	v := uint64(100)
	w := &Watch{stop: make(chan struct{}), read: func() uint64 { return v }}
	w.sample()
	v = 300
	w.sample()
	v = 200
	if got := w.Step(); got != 300 {
		t.Fatalf("first step peak %d", got)
	}
	if got := w.Step(); got != 200 {
		t.Fatalf("a new step starts from now: %d", got)
	}
	if got := w.Peak(); got < 300 {
		t.Fatalf("run peak %d", got)
	}
	var none *Watch
	if none.Step() != 0 || none.Peak() != 0 {
		t.Fatal("a nil watch reads zero")
	}
	none.Stop()
}

func TestStartReadsRSS(t *testing.T) {
	t.Parallel()
	w := Start(time.Millisecond)
	defer w.Stop()
	if RSS() == 0 || w.Peak() == 0 {
		t.Fatal("no resident memory read")
	}
	if MiB(3<<20) != 3 || MiB(1<<19) != 1 {
		t.Fatal("MiB rounds to the nearest")
	}
}
