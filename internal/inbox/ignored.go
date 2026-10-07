package inbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// Ignored messages are remembered this long so status can say what was dropped
const keepIgnored = 3 * 24 * time.Hour

// A message that was read and not queued
// The text is never kept since it may hold anything
type Ignored struct {
	Link       string    `json:"link"`
	From       string    `json:"from,omitempty"`
	Delegation string    `json:"delegation,omitempty"`
	Reason     string    `json:"reason"`
	At         time.Time `json:"at"`
}

func (s Store) ignoredFile() string { return filepath.Join(s.dir, "ignored.jsonl") }

func (s Store) Ignore(r Ignored, now time.Time) error {
	r.At = now.UTC()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return fileio.AppendLine(s.ignoredFile(), b)
}

// Newest first within keepIgnored
// limit 0 lists every one
func (s Store) IgnoredSince(now time.Time, limit int) ([]Ignored, error) {
	f, err := os.Open(s.ignoredFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Ignored
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Ignored
		if json.Unmarshal(sc.Bytes(), &r) == nil && now.Sub(r.At) <= keepIgnored {
			out = append(out, r)
		}
	}
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, sc.Err()
}

// Drops records past keepIgnored and lines that do not parse
func (s Store) PruneIgnored(now time.Time) error {
	_, err := fileio.RewriteLines(s.ignoredFile(), func(lines [][]byte) [][]byte {
		var kept [][]byte
		for _, l := range lines {
			var r Ignored
			if json.Unmarshal(l, &r) == nil && now.Sub(r.At) <= keepIgnored {
				kept = append(kept, l)
			}
		}
		return kept
	})
	return err
}
