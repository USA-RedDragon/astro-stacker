//go:build windows

package stacking

import "golang.org/x/sys/windows"

// freeSpace is the bytes the process may still write to the volume holding
// dir.
func freeSpace(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, err
	}
	return int64(free), nil
}
