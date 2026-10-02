package locmap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type Entry struct {
	RelayId    string    `json:"relay_id"`
	Topic      string    `json:"topic"`
	Keywords   []string  `json:"keywords"`
	Paths      []Path    `json:"paths"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Candidate struct {
	Path
	Score  int
	Topics []string
}

type Map struct{ file string }

func New(dataDir string) Map { return Map{file: filepath.Join(dataDir, "map.jsonl")} }

func (m Map) Add(e Entry) error {
	if err := os.MkdirAll(filepath.Dir(m.file), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Close())
}

// Paths that no longer exist are dropped
func (m Map) Locate(terms []string, limit int) ([]Candidate, error) {
	entries, err := m.entries()
	if err != nil {
		return nil, err
	}
	byAbs := map[string]*Candidate{}
	for _, e := range entries {
		hits := matchCount(e, terms)
		if hits == 0 {
			continue
		}
		for _, p := range e.Paths {
			c, ok := byAbs[p.Abs()]
			if !ok {
				c = &Candidate{Path: p}
				byAbs[p.Abs()] = c
			}
			c.Score += hits
			if !slices.Contains(c.Topics, e.Topic) {
				c.Topics = append(c.Topics, e.Topic)
			}
		}
	}
	out := make([]Candidate, 0, len(byAbs))
	for _, c := range byAbs {
		if c.Exists() {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Abs() < out[j].Abs()
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// The last record per relay id wins so a retried close does not duplicate
func (m Map) entries() ([]Entry, error) {
	f, err := os.Open(m.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s line %d is corrupt: %w", m.file, line, err)
		}
		if i, ok := at[e.RelayId]; ok && e.RelayId != "" {
			out[i] = e
			continue
		}
		at[e.RelayId] = len(out)
		out = append(out, e)
	}
	return out, sc.Err()
}

// Prefix match lets a Korean particle follow a keyword without matching inside other words
func matchCount(e Entry, terms []string) int {
	topic := strings.ToLower(e.Topic)
	n := 0
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if strings.Contains(topic, t) || keywordMatches(e.Keywords, t) {
			n++
		}
	}
	return n
}

func keywordMatches(keywords []string, term string) bool {
	for _, k := range keywords {
		k = strings.ToLower(k)
		if k != "" && (strings.Contains(k, term) || strings.HasPrefix(term, k)) {
			return true
		}
	}
	return false
}
