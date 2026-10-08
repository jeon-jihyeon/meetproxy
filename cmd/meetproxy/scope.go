package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

// The launcher skips every hook of a session while this directory holds no marker of it
// So every session with a scope keeps a marker here from before its scope begins until a turn ends with none left
// A marker left behind only costs the skip since the binary still decides
func scopeDir(data string) string { return filepath.Join(data, "scope") }

// Outside the marker directory so the lock never reads as a marker
func scopeLock(data string) string { return filepath.Join(data, "scope.lock") }

// The marker holds the session id since its name only keeps the characters a file name may
func markerFile(data, session string) string {
	return filepath.Join(scopeDir(data), unsafeId.ReplaceAllString(session, "-"))
}

// Runs begin after marking the session and under the scope lock
// 1. A turn ending meanwhile never removes the marker of a scope begin is creating
// 2. A begin that left no scope such as a claim another session won takes the marker back
func withScope(data, session string, now time.Time, begin func() error) error {
	unlock, err := fileio.Lock(scopeLock(data))
	if err != nil {
		return err
	}
	defer unlock()
	if err := buildScopeDir(data, now); err != nil {
		return err
	}
	if err := fileio.WriteAtomic(markerFile(data, session), []byte(session), 0o600); err != nil {
		return err
	}
	return errors.Join(begin(), release(data, session, now))
}

// Removes the marker of a session that no longer has a scope
func releaseScope(data, session string, now time.Time) error {
	unlock, err := fileio.Lock(scopeLock(data))
	if err != nil {
		return err
	}
	defer unlock()
	return release(data, session, now)
}

// Callers hold the scope lock
func release(data, session string, now time.Time) error {
	_, scoped, err := scopeOf(data, session, now)
	if err != nil || scoped {
		return err
	}
	err = os.Remove(markerFile(data, session))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Builds the marker directory when state from before markers has none yet
// Without it the launcher could skip a session that already handles a request
func ensureScopeDir(data string, now time.Time) error {
	if _, err := os.Stat(scopeDir(data)); err == nil {
		return nil
	}
	unlock, err := fileio.Lock(scopeLock(data))
	if err != nil {
		return err
	}
	defer unlock()
	return buildScopeDir(data, now)
}

// Marks every session that may have a scope in a new directory renamed into place
// So the launcher never sees the directory before its markers
// Callers hold the scope lock
func buildScopeDir(data string, now time.Time) error {
	if _, err := os.Stat(scopeDir(data)); err == nil {
		return nil
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(data, "scope.*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	sessions, err := scopedSessions(data, now)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		file := filepath.Join(tmp, filepath.Base(markerFile(data, s)))
		if err := os.WriteFile(file, []byte(s), 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, scopeDir(data))
}

// Sessions with an open relay, an ended record or a take
// Stale ones are kept since a marker too many only costs the skip
func scopedSessions(data string, now time.Time) ([]string, error) {
	var out []string
	for _, dir := range []string{"open", "ended"} {
		entries, err := os.ReadDir(filepath.Join(data, "relay", dir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if s, ok := strings.CutSuffix(e.Name(), ".json"); ok {
				out = append(out, s)
			}
		}
	}
	items, err := inbox.New(data).List()
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Status == inbox.StatusTaken && !it.Waiting(now) {
			out = append(out, it.SessionId)
		}
	}
	return out, nil
}

// Removes the markers of sessions whose scope ended without a turn ending after it
func pruneScope(data string, now time.Time) error {
	unlock, err := fileio.Lock(scopeLock(data))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(scopeDir(data))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(scopeDir(data), e.Name()))
		if err != nil {
			continue
		}
		if err := release(data, string(b), now); err != nil {
			return err
		}
	}
	return nil
}
