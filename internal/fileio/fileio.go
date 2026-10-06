// Package fileio writes, reads and locks the files that keep state across sessions
package fileio

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Replaces path through a synced temp file in the same directory so a reader never sees half a file
// Missing parent directories are created
func WriteAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err := errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(b, '\n'), 0o644)
}

// Reports false without an error when path does not exist
func ReadJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

// Waits for an exclusive lock on path
// 1. The kernel drops the lock with the process so a crash never leaves it held
// 2. The file stays after unlock since removing it would let a second locker lock a new file while the first still holds the old one
func Lock(path string) (unlock func(), err error) {
	return lock(path, syscall.LOCK_EX)
}

// Takes the lock of Lock only when no one holds it
func TryLock(path string) (unlock func(), ok bool, err error) {
	unlock, err = lock(path, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, false, nil
	}
	return unlock, err == nil, err
}

func lock(path string, how int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return func() { _ = f.Close() }, nil
}

// Appends line and a newline in one write so lines of concurrent writers never interleave
// Missing parent directories are created
func AppendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line[:len(line):len(line)], '\n'))
	return errors.Join(err, f.Close())
}
