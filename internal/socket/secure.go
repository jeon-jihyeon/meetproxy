package socket

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrBusy     = errors.New("listen already owns the runtime directory")
	ErrSecurity = errors.New("listen needs private user-owned directories and files")
)

const maxTokenBytes = 4096

type Lock struct {
	file *os.File
	root *os.Root
}

func directory(path string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrSecurity
	}
	if create && os.MkdirAll(path, 0o700) != nil {
		return nil, ErrSecurity
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !owned(info) {
		return nil, ErrSecurity
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrSecurity
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, ErrSecurity
	}
	return root, nil
}

func privateFile(root *os.Root, name string, flags int) (*os.File, error) {
	f, err := noFollow(root, name, flags, 0o600)
	if err != nil {
		return nil, ErrSecurity
	}
	info, err := f.Stat()
	linked, lerr := root.Lstat(name)
	if err != nil || lerr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		!owned(info) || !os.SameFile(info, linked) {
		_ = f.Close()
		return nil, ErrSecurity
	}
	return f, nil
}

func Acquire(path string) (*Lock, error) {
	root, err := directory(path, true)
	if err != nil {
		return nil, err
	}
	f, err := privateFile(root, "lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		_ = root.Close()
		return nil, err
	}
	return &Lock{file: f, root: root}, nil
}

func (l *Lock) Close() error { return errors.Join(unlock(l.file), l.file.Close(), l.root.Close()) }

func ReadToken(path string) (string, error) {
	root, err := directory(filepath.Dir(path), false)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := privateFile(root, filepath.Base(path), os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil {
		return "", ErrSecurity
	}
	token := strings.TrimSpace(string(raw))
	if len(raw) > maxTokenBytes || !validToken(token) {
		return "", ErrSecurity
	}
	return token, nil
}

func validToken(token string) bool {
	if len(token) <= len("xapp-") || len(token) > maxTokenBytes || !strings.HasPrefix(token, "xapp-") {
		return false
	}
	for _, value := range token {
		if value < 33 || value > 126 {
			return false
		}
	}
	return true
}
