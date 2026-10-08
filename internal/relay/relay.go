// Package relay tracks the open relay of each session
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// At most one per session
type Relay struct {
	Id        string `json:"id"`
	SessionId string `json:"session_id"`
	Origin    string `json:"origin"`
	// What the request works on such as a pull request
	// Posts may go here as well as to the origin
	Target   string    `json:"target,omitempty"`
	OpenedAt time.Time `json:"opened_at"`
}

type Store struct{ dir string }

func New(dataDir string) Store { return Store{dir: filepath.Join(dataDir, "relay")} }

var ErrNoOpen = errors.New("no open relay")

// Open relays left by ended sessions are closed after this
const staleAfter = 7 * 24 * time.Hour

// 1. An open relay of the same origin continues and takes a newly given target
// 2. An open relay of another origin is closed first
func (s Store) Open(sessionId, origin, target string, now time.Time) (Relay, error) {
	cur, err := s.Current(sessionId)
	switch {
	case err == nil && cur.Origin == origin:
		if target == "" || target == cur.Target {
			return cur, nil
		}
		cur.Target = target
		return cur, s.save(cur)
	case err == nil:
		if _, err := s.Close(sessionId, now); err != nil {
			return Relay{}, err
		}
	case !errors.Is(err, ErrNoOpen):
		return Relay{}, err
	}
	s.prune(now)
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return Relay{}, err
	}
	r := Relay{
		Id:        now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b),
		SessionId: sessionId,
		Origin:    origin,
		Target:    target,
		OpenedAt:  now.UTC(),
	}
	return r, s.save(r)
}

// Atomic so the posting guard never reads half a relay
func (s Store) save(r Relay) error {
	return fileio.WriteJSON(s.openFile(r.SessionId), r)
}

func (s Store) Current(sessionId string) (Relay, error) {
	var r Relay
	found, err := fileio.ReadJSON(s.openFile(sessionId), &r)
	if err != nil {
		return Relay{}, fmt.Errorf("%s is unreadable, remove it to reset the relay: %w", s.openFile(sessionId), err)
	}
	if !found {
		return Relay{}, ErrNoOpen
	}
	return r, nil
}

// Best effort since a stale relay only guards a session that has ended
// Ended records past their hour are removed with them
func (s Store) prune(now time.Time) {
	for _, e := range entries(filepath.Join(s.dir, "open"), now, staleAfter) {
		_, _ = s.Close(strings.TrimSuffix(e, ".json"), now)
	}
	for _, e := range entries(filepath.Join(s.dir, "ended"), now, lingerFor) {
		_ = os.Remove(filepath.Join(s.dir, "ended", e))
	}
}

// Closed records older than this are removed by Prune
const keepClosed = 90 * 24 * time.Hour

// Removes state that only grows
// 1. Stale open relays and ended records as Open does
// 2. Paths the location map observed since nothing reads them any more
// 3. Closed records older than keepClosed
func (s Store) Prune(now time.Time) error {
	s.prune(now)
	if err := os.RemoveAll(filepath.Join(s.dir, "observed")); err != nil {
		return err
	}
	_, err := fileio.RewriteLines(filepath.Join(s.dir, "closed.jsonl"), func(lines [][]byte) [][]byte {
		var kept [][]byte
		for _, l := range lines {
			var rec closed
			// A line that does not parse is kept since only the user can tell what it held
			if json.Unmarshal(l, &rec) != nil || now.Sub(rec.ClosedAt) <= keepClosed {
				kept = append(kept, l)
			}
		}
		return kept
	})
	return err
}

// Names of the files in dir last written more than age ago
func entries(dir string, now time.Time, age time.Duration) []string {
	all, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range all {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= age {
			out = append(out, e.Name())
		}
	}
	return out
}

// A relay as it closed
type closed struct {
	Relay
	ClosedAt time.Time `json:"closed_at"`
}

// An ended record older than this no longer keeps its scope since the turn that closed it is long over
const lingerFor = time.Hour

// Keeps the scope of the relay until the turn ends
// So the turn that closed it never posts anywhere a request text names
func (s Store) Close(sessionId string, now time.Time) (Relay, error) {
	r, err := s.Current(sessionId)
	if err != nil {
		return Relay{}, err
	}
	rec := closed{r, now.UTC()}
	if err := appendLine(filepath.Join(s.dir, "closed.jsonl"), rec); err != nil {
		return Relay{}, err
	}
	if err := fileio.WriteJSON(s.endedFile(sessionId), rec); err != nil {
		return Relay{}, err
	}
	return r, os.Remove(s.openFile(sessionId))
}

// Keeps the scope of a request settled without a relay until the turn ends
func (s Store) Linger(sessionId, origin, target string, now time.Time) error {
	return fileio.WriteJSON(s.endedFile(sessionId), closed{Relay{SessionId: sessionId, Origin: origin, Target: target}, now.UTC()})
}

// The relay or request the session settled in the turn still running
func (s Store) Ended(sessionId string, now time.Time) (Relay, bool, error) {
	var rec closed
	found, err := fileio.ReadJSON(s.endedFile(sessionId), &rec)
	if err != nil {
		return Relay{}, false, fmt.Errorf("%s is unreadable, remove it to reset the relay: %w", s.endedFile(sessionId), err)
	}
	if !found || now.Sub(rec.ClosedAt) > lingerFor {
		return Relay{}, false, nil
	}
	return rec.Relay, true, nil
}

func (s Store) EndTurn(sessionId string) error {
	err := os.Remove(s.endedFile(sessionId))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) openFile(sessionId string) string {
	return filepath.Join(s.dir, "open", sessionId+".json")
}

func (s Store) endedFile(sessionId string) string {
	return filepath.Join(s.dir, "ended", sessionId+".json")
}

func appendLine(file string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return fileio.AppendLine(file, b)
}
