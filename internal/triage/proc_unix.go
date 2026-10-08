//go:build !windows

package triage

import (
	"os/exec"
	"syscall"
)

// Kills the whole process group of cmd on timeout
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
