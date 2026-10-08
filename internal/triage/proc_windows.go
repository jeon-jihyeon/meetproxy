//go:build windows

package triage

import "os/exec"

// Windows has no process group to kill so the timeout kills cmd alone
func killGroup(*exec.Cmd) {}
