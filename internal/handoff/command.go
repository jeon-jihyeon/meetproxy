package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	maxInput  = 4 << 20
	maxOutput = 64 << 10
	maxWait   = 2 * time.Second
)

var ErrCommand = errors.New("handoff command did not confirm durable receipt")

type Command struct {
	binary  string
	args    []string
	env     []string
	timeout time.Duration
}

func New(binary string, arguments, environment []string, timeout time.Duration) (*Command, error) {
	if !filepath.IsAbs(binary) || strings.ContainsRune(binary, 0) || timeout < 0 {
		return nil, ErrCommand
	}
	for _, value := range arguments {
		if strings.ContainsRune(value, 0) {
			return nil, ErrCommand
		}
	}
	if timeout == 0 || timeout > maxWait {
		timeout = maxWait
	}
	return &Command{
		binary: binary, args: slices.Clone(arguments), env: allowedEnvironment(environment), timeout: timeout,
	}, nil
}

func (c *Command) Send(ctx context.Context, body []byte) error {
	if len(body) > maxInput || !json.Valid(body) {
		return ErrCommand
	}
	budget, cancelBudget := context.WithTimeout(ctx, c.timeout)
	defer cancelBudget()
	deadline, _ := budget.Deadline()
	reserve := min(25*time.Millisecond, time.Until(deadline)/4)
	if reserve <= 0 || budget.Err() != nil {
		return ErrCommand
	}
	process, cancel := context.WithDeadline(budget, deadline.Add(-reserve))
	defer cancel()
	discard := &limitedDiscard{cancel: cancel}
	cmd := exec.CommandContext(process, c.binary, c.args...)
	cmd.Env = slices.Clone(c.env)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout, cmd.Stderr = discard, discard
	cmd.WaitDelay = reserve
	err := cmd.Run()
	if err != nil || budget.Err() != nil || process.Err() != nil || discard.exceeded() {
		return ErrCommand
	}
	return nil
}

type limitedDiscard struct {
	mu     sync.Mutex
	bytes  int
	full   bool
	cancel context.CancelFunc
}

func (d *limitedDiscard) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(p) > maxOutput-d.bytes {
		d.full = true
		d.cancel()
	} else {
		d.bytes += len(p)
	}
	return len(p), nil
}

func (d *limitedDiscard) exceeded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.full
}

func allowedEnvironment(environment []string) []string {
	env := make([]string, 0)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.ContainsRune(entry, 0) {
			continue
		}
		switch key {
		case "PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT", "WINDIR",
			"TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE":
			env = append(env, entry)
		}
	}
	return env
}
