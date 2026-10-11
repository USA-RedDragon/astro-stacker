//go:build !windows

package stacking

import (
	"math"
	"syscall"
)

// freeSpace is the bytes an unprivileged process may still write to the
// filesystem holding dir.
func freeSpace(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	free := st.Bavail * uint64(max(st.Bsize, 0))
	if free > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}
