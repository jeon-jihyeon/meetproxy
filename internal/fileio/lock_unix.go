//go:build !windows

package fileio

import (
	"errors"
	"os"
	"syscall"
)

// Reports false without an error when another process holds the lock and wait is not set
func lockFile(f *os.File, wait bool) (bool, error) {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), how)
	if !wait && errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
