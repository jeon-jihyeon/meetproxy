//go:build windows

package opstore

import (
	"os"
)

func tryLock(_ *os.File) (bool, error) {
	return false, ErrUnsupported
}
