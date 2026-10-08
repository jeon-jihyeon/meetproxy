// Package fileio writes, reads and locks the files that keep state across sessions
package fileio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The state holds request text, delegations and a Slack token so only the user may read it
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Replaces path through a synced temp file in the same directory so a reader never sees half a file
// Missing parent directories are created
func WriteAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
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
	return WriteAtomic(path, append(b, '\n'), filePerm)
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
	unlock, _, err = lock(path, true)
	return unlock, err
}

// Takes the lock of Lock only when no one holds it
func TryLock(path string) (unlock func(), ok bool, err error) {
	return lock(path, false)
}

func lock(path string, wait bool) (func(), bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, false, err
	}
	locked, err := lockFile(f, wait)
	if err != nil || !locked {
		return nil, false, errors.Join(err, f.Close())
	}
	return func() { _ = f.Close() }, true, nil
}

// Appends line and a newline in one write so lines of concurrent writers never interleave
// Missing parent directories are created
func AppendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line[:len(line):len(line)], '\n'))
	return errors.Join(err, f.Close())
}

// Moves a file that cannot be read to <dir>/corrupt/<name>.<unix seconds> and returns where it went
// Kept rather than removed so the user can see what broke
func Quarantine(path string) (string, error) {
	dir := filepath.Join(filepath.Dir(path), "corrupt")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	to := filepath.Join(dir, filepath.Base(path)+"."+strconv.FormatInt(time.Now().Unix(), 10))
	return to, os.Rename(path, to)
}

// Restricts every directory under root to the user and every file to the user's reads and writes
// State written before files were private keeps its old permissions until this runs
// Symbolic links are left as they are since their target may lie outside root
func Restrict(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return os.Chmod(path, dirPerm)
		default:
			return os.Chmod(path, filePerm)
		}
	})
}

// Replaces the lines of path with what rewrite returns for them and reports whether it wrote
// 1. A missing file or an unchanged count of lines writes nothing
// 2. Lines appended while rewrite ran would be lost so the file is left as it is when it grew meanwhile
func RewriteLines(path string, rewrite func(lines [][]byte) [][]byte) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(b) == 0 {
		lines = nil
	}
	kept := rewrite(lines)
	if len(kept) == len(lines) {
		return false, nil
	}
	var out []byte
	for _, l := range kept {
		out = append(append(out, l...), '\n')
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(out)
	if err == nil {
		err = tmp.Sync()
	}
	if err == nil {
		err = tmp.Chmod(filePerm)
	}
	if err := errors.Join(err, tmp.Close()); err != nil {
		return false, err
	}
	if info, err := os.Stat(path); err != nil || info.Size() != int64(len(b)) {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}
