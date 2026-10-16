// Package memlimit sets the Go runtime's soft memory limit from the
// container's cgroup memory limit. Go otherwise lets the heap grow to twice
// what is live before it collects: a discoveries run holding 2.6 GB live
// reached a 5.2 GB heap, and runs of larger fields were killed at an 8 GiB
// limit.
package memlimit

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// Share is the part of the cgroup limit the heap is held to; the rest is
// for the runtime's own memory and what is not the Go heap.
const Share = 0.85

// cgroupFiles are where cgroup v2 and v1 keep the memory limit.
var cgroupFiles = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} //nolint:gochecknoglobals

// Set sets the soft memory limit to Share of the cgroup's memory limit,
// unless GOMEMLIMIT is set or there is no limit, and returns the limit set
// in bytes, 0 when it set none.
func Set() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	for _, f := range cgroupFiles {
		if n := Limit(f); n > 0 {
			l := int64(float64(n) * Share)
			debug.SetMemoryLimit(l)
			return l
		}
	}
	return 0
}

// Limit reads a cgroup memory limit file, 0 when it holds none.
func Limit(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0
	}
	return n
}
