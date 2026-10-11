//go:build !windows

package astrometry

import (
	"os/exec"
	"syscall"
)

// killGroup makes a cancelled solve also stop the astrometry-engine
// solve-field starts.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
