package memwatch

import (
	"testing"
	"time"
)

func TestStepAndPeak(t *testing.T) {
	t.Parallel()
	v := Peaks{100, 50}
	w := &Watch{stop: make(chan struct{}), read: func() Peaks { return v }}
	w.sample()
	v = Peaks{300, 40}
	w.sample()
	v = Peaks{200, 60}
	if got := w.Step(); got != (Peaks{300, 60}) {
		t.Fatalf("first step peaks %+v", got)
	}
	if got := w.Step(); got != (Peaks{200, 60}) {
		t.Fatalf("a new step starts from now: %+v", got)
	}
	if got := w.Peak(); got.RSS < 300 || got.Live != 60 {
		t.Fatalf("run peaks %+v", got)
	}
	var none *Watch
	if none.Step() != (Peaks{}) || none.Peak() != (Peaks{}) {
		t.Fatal("a nil watch reads zero")
	}
	none.Stop()
}

func TestStartReadsRSS(t *testing.T) {
	t.Parallel()
	w := Start(time.Millisecond)
	defer w.Stop()
	if RSS() == 0 || w.Peak().RSS == 0 {
		t.Fatal("no resident memory read")
	}
	if MiB(3<<20) != 3 || MiB(1<<19) != 1 {
		t.Fatal("MiB rounds to the nearest")
	}
}
