//go:build !windows

package socket

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func owned(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Getuid())
}

func noFollow(root *os.Root, name string, flags int, mode fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, mode)
}

func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrBusy
	}
	if err != nil {
		return ErrSecurity
	}
	return nil
}

func unlock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return ErrSecurity
	}
	return nil
}
