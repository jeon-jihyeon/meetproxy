//go:build !windows

package signinbrowser

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func SystemRun() func(context.Context, string) (string, error) {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return run
}

func Command(ctx context.Context, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript")
	// Callback codes stay off the process list
	cmd.Stdin = strings.NewReader(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd
}

func run(ctx context.Context, script string) (string, error) {
	cmd := Command(ctx, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// Script errors may quote callback codes
		if strings.Contains(stderr.String(), "(-1743)") {
			return "", ErrAutomationDenied
		}
		return "", ErrScript
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}
