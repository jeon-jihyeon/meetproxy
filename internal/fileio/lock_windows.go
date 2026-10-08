//go:build windows

package fileio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// The standard library has no LockFileEx so it is called from kernel32 the way golang.org/x/sys does
var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

// Reports false without an error when another process holds the lock and wait is not set
// Locks the first byte which every locker names so the lock covers the file whatever it holds
func lockFile(f *os.File, wait bool) (bool, error) {
	flags := uintptr(lockfileExclusiveLock)
	if !wait {
		flags |= lockfileFailImmediately
	}
	var ol syscall.Overlapped
	r, _, err := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return true, nil
	}
	if !wait && errors.Is(err, errorLockViolation) {
		return false, nil
	}
	return false, err
}
