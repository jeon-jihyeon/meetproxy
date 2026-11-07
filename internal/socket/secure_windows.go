//go:build windows

package socket

import (
	"io/fs"
	"os"
)

func owned(fs.FileInfo) bool { return false }

func noFollow(*os.Root, string, int, fs.FileMode) (*os.File, error) { return nil, ErrSecurity }

func tryLock(*os.File) error { return ErrSecurity }

func unlock(*os.File) error { return ErrSecurity }
