//go:build !windows

package main

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Browser scripting runs through osascript on macOS only
func osBrowser() browserScript {
	b := browserScript{clock: time.Now, sleep: time.Sleep}
	if runtime.GOOS == "darwin" {
		b.run = osascript
	}
	return b
}

// The script goes on stdin so a process list shows neither the link nor the address
func osascriptCommand(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript")
	cmd.Stdin = strings.NewReader(script)
	// A timeout kills the whole group and WaitDelay stops a child holding the pipes from keeping Wait waiting
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd
}

// stderr only picks the error, since an AppleScript error can quote the script
func osascript(ctx context.Context, script string) (string, error) {
	cmd := osascriptCommand(ctx, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "(-1743)") {
			return "", errAutomationDenied
		}
		return "", errBrowserScript
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}
