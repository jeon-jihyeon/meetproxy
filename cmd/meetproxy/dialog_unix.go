//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Starts cmd as the leader of its own process group
// A signal to this process's group never reaches the passcode dialog, which outlives the call
func newGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Kills the whole group of a cmd made by CommandContext when its context ends
// so no child the dialog program started is left behind
func ownGroup(cmd *exec.Cmd) {
	newGroup(cmd)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// Kills the process group a dialog of program leads
// 1. pid 0 and 1 would name this process's own group or init, so they are never signalled
// 2. A pid whose dialog already ended may name another group by now, so the group is killed only while it still leads it running program
// 3. Other dialogs run the same program, so the start time must match too
func killGroup(d shownDialog, program string) {
	if d.Pid > 1 && d.Started != "" && leadsDialog(d, program) {
		_ = syscall.Kill(-d.Pid, syscall.SIGKILL)
	}
}

func leadsDialog(d shownDialog, program string) bool {
	if pgid, err := syscall.Getpgid(d.Pid); err != nil || pgid != d.Pid || startedAt(d.Pid) != d.Started {
		return false
	}
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(d.Pid)).Output()
	return err == nil && strings.Contains(string(out), program)
}

// The start time ps gives a process, empty when it can't be read
func startedAt(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
