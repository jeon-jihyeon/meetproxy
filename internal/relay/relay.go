// Package relay tracks the open relay of each session
package relay

import (
	"bufio"
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
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
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
func (s Store) prune(now time.Time) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "open"))
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleAfter {
			continue
		}
		_, _ = s.Close(strings.TrimSuffix(e.Name(), ".json"), now)
	}
}

func (s Store) Observe(sessionId string, p locmap.Path) error {
	r, err := s.Current(sessionId)
	if errors.Is(err, ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	return appendLine(s.observedFile(r.Id), p)
}

func (s Store) Observed(relayId string) ([]locmap.Path, error) {
	file := s.observedFile(relayId)
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	seen := map[string]bool{}
	var out []locmap.Path
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		var p locmap.Path
		if err := json.Unmarshal(sc.Bytes(), &p); err != nil {
			return nil, fmt.Errorf("%s line %d is corrupt: %w", file, line, err)
		}
		if seen[p.Abs()] {
			continue
		}
		seen[p.Abs()] = true
		out = append(out, p)
	}
	return out, sc.Err()
}

// Falls back to observed paths when no given path resolves
// 1. Paths outside any repository belong to the session directory dir as their place
// 2. A path in neither is skipped since closing must never fail on evidence
func (s Store) Evidence(relayId string, raws []string, dir string) ([]locmap.Path, error) {
	ps := make([]locmap.Path, 0, len(raws))
	for _, raw := range raws {
		if p, ok := locmap.ResolveIn(raw, dir); ok {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return s.Observed(relayId)
	}
	return ps, nil
}

func (s Store) Close(sessionId string, now time.Time) (Relay, error) {
	r, err := s.Current(sessionId)
	if err != nil {
		return Relay{}, err
	}
	rec := struct {
		Relay
		ClosedAt time.Time `json:"closed_at"`
	}{r, now.UTC()}
	if err := appendLine(filepath.Join(s.dir, "closed.jsonl"), rec); err != nil {
		return Relay{}, err
	}
	return r, os.Remove(s.openFile(sessionId))
}

func (s Store) openFile(sessionId string) string {
	return filepath.Join(s.dir, "open", sessionId+".json")
}

func (s Store) observedFile(relayId string) string {
	return filepath.Join(s.dir, "observed", relayId+".jsonl")
}

func appendLine(file string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return fileio.AppendLine(file, b)
}
