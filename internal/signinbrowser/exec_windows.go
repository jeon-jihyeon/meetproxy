//go:build windows

package signinbrowser

import (
	"context"
	"os/exec"
)

func SystemRun() func(context.Context, string) (string, error) { return nil }

func Command(ctx context.Context, _ string) *exec.Cmd {
	return exec.CommandContext(ctx, "/usr/bin/osascript")
}
