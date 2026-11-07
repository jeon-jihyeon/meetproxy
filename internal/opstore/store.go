package opstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const valueLimit = (16 << 20) + (1 << 10)
const rowLimit = 24 << 20

var (
	ErrConflict    = errors.New("operation fingerprint conflict")
	ErrInput       = errors.New("invalid operation record")
	ErrPath        = errors.New("unsafe operation store path")
	ErrUnsupported = errors.New("local operation journals require a Unix platform")
)

type Store struct {
	dir string
}

type row struct {
	Fingerprint string `json:"fingerprint"`
	Value       []byte `json:"value"`
}

func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Claim(ctx context.Context, id, fingerprint string, initial []byte) ([]byte, bool, error) {
	name, err := recordName(id)
	if err != nil || !validValue(fingerprint, initial) {
		return nil, false, ErrInput
	}
	r, unlock, err := s.lock(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	stored, exists, err := readCurrent(r, name)
	if err != nil {
		return nil, false, err
	}
	if exists {
		if stored.Fingerprint != fingerprint {
			return nil, false, ErrConflict
		}
		return stored.Value, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := s.write(r, name+".json", row{Fingerprint: fingerprint, Value: initial}); err != nil {
		return nil, false, err
	}
	return bytes.Clone(initial), true, nil
}

func (s *Store) Save(ctx context.Context, id, fingerprint string, result []byte) error {
	name, err := recordName(id)
	if err != nil || !validValue(fingerprint, result) {
		return ErrInput
	}
	r, unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	intent, exists, err := readRow(r, name+".json")
	if err != nil {
		return err
	}
	if !exists {
		return fs.ErrNotExist
	}
	if intent.Fingerprint != fingerprint {
		return ErrConflict
	}
	stored, found, err := readRow(r, name+".result.json")
	if err != nil {
		return err
	}
	if found && stored.Fingerprint != fingerprint {
		return ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.write(r, name+".result.json", row{Fingerprint: fingerprint, Value: result})
}

func (s *Store) Read(ctx context.Context, id string) ([]byte, bool, error) {
	name, err := recordName(id)
	if err != nil {
		return nil, false, err
	}
	r, unlock, err := s.lock(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	stored, exists, err := readCurrent(r, name)
	if err != nil || !exists {
		return nil, exists, err
	}
	return stored.Value, true, nil
}

func recordName(id string) (string, error) {
	if id == "" || len(id) > 4096 {
		return "", ErrInput
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]), nil
}

func validValue(fingerprint string, value []byte) bool {
	return fingerprint != "" && len(fingerprint) <= 1024 && len(value) > 0 && len(value) <= valueLimit
}

func readCurrent(r *os.Root, name string) (row, bool, error) {
	intent, exists, err := readRow(r, name+".json")
	if err != nil {
		return row{}, exists, err
	}
	if !exists {
		if _, err := r.Lstat(name + ".result.json"); !errors.Is(err, fs.ErrNotExist) {
			return row{}, true, ErrInput
		}
		return row{}, false, nil
	}
	result, found, err := readRow(r, name+".result.json")
	if err != nil {
		return row{}, true, err
	}
	if found {
		if result.Fingerprint != intent.Fingerprint {
			return row{}, true, ErrConflict
		}
		return result, true, nil
	}
	return intent, true, nil
}

func readRow(r *os.Root, name string) (row, bool, error) {
	info, err := r.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > rowLimit {
		return row{}, true, ErrPath
	}
	f, err := r.Open(name)
	if err != nil {
		return row{}, true, err
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return row{}, true, ErrPath
	}
	body, err := io.ReadAll(io.LimitReader(f, rowLimit+1))
	if err != nil || len(body) > rowLimit {
		return row{}, true, ErrInput
	}
	var value row
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&value) != nil || !validValue(value.Fingerprint, value.Value) {
		return row{}, true, ErrInput
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return row{}, true, ErrInput
	}
	return value, true, nil
}

func (s *Store) lock(ctx context.Context) (*os.Root, func(), error) {
	if runtime.GOOS == "windows" {
		return nil, nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	r, err := s.open()
	if err != nil {
		return nil, nil, err
	}
	f, err := openLock(r)
	if err != nil {
		_ = r.Close()
		return nil, nil, err
	}
	closeFiles := func() { _ = f.Close(); _ = r.Close() }
	if err := waitForLock(ctx, r, f); err != nil {
		closeFiles()
		return nil, nil, err
	}
	return r, closeFiles, nil
}

func openLock(r *os.Root) (*os.File, error) {
	f, err := r.OpenFile("lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if !errors.Is(err, fs.ErrExist) {
		return f, err
	}
	info, err := r.Lstat("lock")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, ErrPath
	}
	return r.OpenFile("lock", os.O_RDWR, 0o600)
}

func waitForLock(ctx context.Context, r *os.Root, f *os.File) error {
	for {
		locked, err := tryLock(f)
		if err != nil {
			return err
		}
		if locked {
			return verifyLock(r, f)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func verifyLock(r *os.Root, f *os.File) error {
	info, statErr := r.Lstat("lock")
	actual, fileErr := f.Stat()
	if statErr != nil || fileErr != nil || !info.Mode().IsRegular() || !os.SameFile(info, actual) {
		return ErrPath
	}
	return nil
}

func (s *Store) open() (*os.Root, error) {
	if s.dir == "" {
		return nil, ErrPath
	}
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(abs)
	r, err := os.OpenRoot(volume + string(os.PathSeparator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(abs, volume+string(os.PathSeparator)), string(os.PathSeparator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, err := descend(r, part)
		_ = r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	if err := r.Chmod(".", 0o700); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func descend(r *os.Root, part string) (*os.Root, error) {
	info, err := directoryInfo(r, part)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: directory type", ErrPath)
	}
	next, err := r.OpenRoot(part)
	if err != nil {
		return nil, err
	}
	actual, err := next.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		_ = next.Close()
		return nil, fmt.Errorf("%w: directory identity", ErrPath)
	}
	return next, nil
}

func directoryInfo(r *os.Root, part string) (fs.FileInfo, error) {
	info, err := r.Lstat(part)
	if !errors.Is(err, fs.ErrNotExist) {
		return info, err
	}
	created := r.Mkdir(part, 0o700)
	if created != nil && !errors.Is(created, fs.ErrExist) {
		return nil, created
	}
	if created == nil {
		if err := syncDirectory(r); err != nil {
			return nil, err
		}
	}
	return r.Lstat(part)
}

func (s *Store) write(r *os.Root, name string, value row) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	tmp := hex.EncodeToString(random) + ".tmp"
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = r.Remove(tmp) }()
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if info, err := r.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return ErrPath
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return s.replace(r, tmp, name)
}
