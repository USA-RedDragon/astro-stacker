// Package memwatch samples the process's resident memory and the Go
// heap left live by the last collection, so a job can log the peaks of
// each of its steps and of the whole run. Resident memory shows what the
// kernel charged; the live heap shows what the job needed, as the
// collector lets the heap grow past it before it collects.
package memwatch

import (
	"bytes"
	"os"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Peaks are the highest resident memory and live heap seen, in bytes.
type Peaks struct {
	RSS  uint64
	Live uint64
}

func (p Peaks) max(q Peaks) Peaks { return Peaks{max(p.RSS, q.RSS), max(p.Live, q.Live)} }

// Watch keeps the peaks seen since Start and since the last Step. A nil
// Watch reads as zero.
type Watch struct {
	mu   sync.Mutex
	step Peaks
	all  Peaks
	stop chan struct{}
	read func() Peaks
}

// Start samples every interval until Stop.
func Start(every time.Duration) *Watch {
	w := &Watch{stop: make(chan struct{}), read: func() Peaks { return Peaks{RSS(), Live()} }}
	w.sample()
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				w.sample()
			}
		}
	}()
	return w
}

func (w *Watch) sample() {
	v := w.read()
	w.mu.Lock()
	w.step = w.step.max(v)
	w.all = w.all.max(v)
	w.mu.Unlock()
}

// Step is the peaks since the last Step, and starts the next one.
func (w *Watch) Step() Peaks {
	if w == nil {
		return Peaks{}
	}
	w.sample()
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.step
	w.step = Peaks{}
	return v
}

// Peak is the peaks since Start, resident memory taken as the sampled
// peak or the kernel's own high water mark, whichever is higher.
func (w *Watch) Peak() Peaks {
	if w == nil {
		return Peaks{}
	}
	w.sample()
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.all.max(Peaks{RSS: hwm()})
}

// Stop ends the sampling.
func (w *Watch) Stop() {
	if w != nil {
		close(w.stop)
	}
}

// MiB is bytes in whole mebibytes, for logs.
func MiB(b uint64) uint64 { return (b + 1<<19) >> 20 }

// RSS is the process's resident memory, from /proc, or else the Go
// runtime's total when /proc can't be read.
func RSS() uint64 {
	if v, ok := status("VmRSS:"); ok {
		return v
	}
	s := []metrics.Sample{{Name: "/memory/classes/total:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() == metrics.KindUint64 {
		return s[0].Value.Uint64()
	}
	return 0
}

// Live is the Go heap the last collection found live.
func Live() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() == metrics.KindUint64 {
		return s[0].Value.Uint64()
	}
	return 0
}

func hwm() uint64 {
	v, _ := status("VmHWM:")
	return v
}

// status reads one kB field of /proc/self/status, in bytes.
func status(field string) (uint64, bool) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte(field)); ok {
			f := strings.Fields(string(v))
			if len(f) > 0 {
				if kb, err := strconv.ParseUint(f[0], 10, 64); err == nil {
					return kb << 10, true
				}
			}
		}
	}
	return 0, false
}
