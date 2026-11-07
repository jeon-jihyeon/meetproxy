//go:build !windows

package socket

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	if len(os.Args) != 2 || os.Args[1] != "socket-lock-fixture" {
		return
	}
	file := os.NewFile(3, "fixture-lock")
	if _, err := file.Stat(); err != nil {
		os.Exit(1)
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := file.Close(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLockCloseWithInheritedDescriptor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	lock, err := Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "socket-lock-fixture")
	cmd.ExtraFiles = []*os.File{lock.file}
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Wait() })
	ready, err := bufio.NewReader(output).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	require.NoError(t, lock.Close())
	next, err := Acquire(dir)
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestFailedAcquireKeepsOwner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	owner, err := Acquire(dir)
	require.NoError(t, err)
	defer func() { _ = owner.Close() }()
	for range 2 {
		lock, err := Acquire(dir)
		assert.ErrorIs(t, err, ErrBusy)
		assert.Nil(t, lock)
	}
}

func TestLockCloseErrors(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		init func(*testing.T, *Lock)
		want bool
	}{
		{"closed file reports an error", func(t *testing.T, lock *Lock) { require.NoError(t, lock.file.Close()) }, true},
		{"closed directory stays idempotent", func(t *testing.T, lock *Lock) {
			require.NoError(t, lock.root.Close())
		}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			lock, err := Acquire(dir)
			require.NoError(t, err)
			tc.init(t, lock)
			assert.Equal(t, tc.want, lock.Close() != nil)
			_, err = lock.root.Stat(".")
			assert.Error(t, err)
			next, err := Acquire(dir)
			require.NoError(t, err)
			assert.NoError(t, next.Close())
		})
	}
}
