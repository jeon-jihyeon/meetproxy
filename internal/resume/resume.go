// Package resume keeps the continuations of mod chains, one file per token named by its sha256
package resume

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// How long a continuation lasts when its chain sets no shorter life
const DefaultLife = 24 * time.Hour

// An unknown, used, expired or other session's token
// The error names none of them so a caller learns nothing about another session's chains
var ErrUnknown = errors.New("this step is unknown, used or expired")

// An expired token of the caller's own session
// It reads as ErrUnknown and comes back with its continuation so its chain can end what it left outside
var ErrExpired = fmt.Errorf("%w", ErrUnknown)

var validToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

// One step of a chain waiting for the host
// 1. Never a token, a password or a passcode
// 2. Chain names the step the outcome continues
type Continuation struct {
	Session string          `json:"session"`
	Host    json.RawMessage `json:"host,omitempty"`
	Chain   string          `json:"chain"`
	State   json.RawMessage `json:"state"`
	Expires time.Time       `json:"expires"`
}

type Store struct{ dir string }

func New(dataDir string) Store { return Store{dir: filepath.Join(dataDir, "mod")} }

func (s Store) resumeDir() string { return filepath.Join(s.dir, "resume") }

// A listing of the directory or a process list never shows a token since only its digest names the file
func (s Store) file(token string) string {
	sum := sha256.Sum256([]byte(token))
	return filepath.Join(s.resumeDir(), hex.EncodeToString(sum[:])+".json")
}

func (s Store) lock() (func(), error) { return fileio.Lock(filepath.Join(s.dir, "lock")) }

// Saves c for life and returns its token
// A life of zero or less is DefaultLife
func (s Store) Put(c Continuation, life time.Duration, now time.Time) (string, error) {
	if life <= 0 {
		life = DefaultLife
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	c.Expires = now.Add(life).UTC()
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	return token, fileio.WriteJSON(s.file(token), c)
}

// The continuation of token for session, removed before it is returned so each token runs once
// 1. Another session's token stays for its own session, an expired one too
// 2. An expired token of session is removed and comes back with ErrExpired
func (s Store) Take(token, session string, now time.Time) (Continuation, error) {
	if !validToken.MatchString(token) {
		return Continuation{}, ErrUnknown
	}
	unlock, err := s.lock()
	if err != nil {
		return Continuation{}, err
	}
	defer unlock()
	file := s.file(token)
	var c Continuation
	found, err := fileio.ReadJSON(file, &c)
	if !found {
		return Continuation{}, ErrUnknown
	}
	if err != nil {
		_ = os.Remove(file)
		return Continuation{}, ErrUnknown
	}
	if c.Session != session {
		return Continuation{}, ErrUnknown
	}
	if err := os.Remove(file); err != nil {
		return Continuation{}, err
	}
	if !now.Before(c.Expires) {
		return c, ErrExpired
	}
	return c, nil
}

// Removes every continuation of session as its end does and returns them, so their chains can end too
func (s Store) DropSession(session string) ([]Continuation, error) {
	return s.sweep(func(c Continuation, ok bool) bool { return ok && c.Session == session })
}

// Removes expired and unreadable continuations and returns the expired ones, so their chains can end too
func (s Store) Prune(now time.Time) ([]Continuation, error) {
	return s.sweep(func(c Continuation, ok bool) bool { return !ok || !now.Before(c.Expires) })
}

// Removes the expired continuations of session and returns them, so their chains can end too
func (s Store) PruneSession(session string, now time.Time) ([]Continuation, error) {
	return s.sweep(func(c Continuation, ok bool) bool { return ok && c.Session == session && !now.Before(c.Expires) })
}

// Whether session has a continuation, an expired one too, since only its removal ends its chain
func (s Store) Holds(session string) bool {
	entries, err := os.ReadDir(s.resumeDir())
	if err != nil {
		return false
	}
	for _, e := range entries {
		var c Continuation
		if found, err := fileio.ReadJSON(filepath.Join(s.resumeDir(), e.Name()), &c); found && err == nil && c.Session == session {
			return true
		}
	}
	return false
}

func (s Store) sweep(drop func(c Continuation, ok bool) bool) ([]Continuation, error) {
	entries, err := os.ReadDir(s.resumeDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	var errs []error
	var dropped []Continuation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		file := filepath.Join(s.resumeDir(), e.Name())
		var c Continuation
		found, err := fileio.ReadJSON(file, &c)
		if !found {
			continue
		}
		if drop(c, err == nil) {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			// An unreadable one names no chain to end
			if err == nil {
				dropped = append(dropped, c)
			}
		}
	}
	return dropped, errors.Join(errs...)
}
