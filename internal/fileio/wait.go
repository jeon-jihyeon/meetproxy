package fileio

import (
	"errors"
	"time"
)

var ErrLockBusy = errors.New("file lock wait timed out")

func WaitLock(path string, wait time.Duration, deadline time.Time, waiting func()) (func(), error) {
	until := time.Now().Add(wait)
	if !deadline.IsZero() && deadline.Before(until) {
		until = deadline
	}
	for waited := false; ; waited = true {
		unlock, locked, err := TryLock(path)
		if err != nil {
			return nil, err
		}
		if locked {
			return unlock, nil
		}
		if !waited && waiting != nil {
			waiting()
		}
		if !time.Now().Before(until) {
			return nil, ErrLockBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}
