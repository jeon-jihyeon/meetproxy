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
	"unicode"
	"unicode/utf8"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type Entry struct {
	RelayId    string    `json:"relay_id"`
	Topic      string    `json:"topic"`
	Keywords   []string  `json:"keywords"`
	Paths      []Path    `json:"paths"`
	RecordedAt time.Time `json:"recorded_at"`
	// How much the record counts
	// A stored 0 counts as 1 and a correction halves it
	Weight float64 `json:"weight,omitempty"`
}

func (e Entry) weight() float64 {
	if e.Weight <= 0 {
		return 1
	}
	return e.Weight
}

type Candidate struct {
	Path
	Score  float64
	Topics []string
}

type Map struct{ file string }

func New(dataDir string) Map { return Map{file: filepath.Join(dataDir, "map.jsonl")} }

func (m Map) Add(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return fileio.AppendLine(m.file, b)
}

// Paths that no longer exist are dropped
func (m Map) Locate(terms []string, limit int) ([]Candidate, error) {
	entries, err := m.entries()
	if err != nil {
		return nil, err
	}
	byAbs := map[string]*Candidate{}
	for _, e := range entries {
		hits := float64(matchCount(e, terms)) * e.weight()
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

// Root of the place where most similar answers were found
// 1. Each answer counts once per place however many files it rests on
// 2. Ties go to the root that sorts first and no match returns an empty root
func (m Map) Route(terms []string) (string, error) {
	entries, err := m.entries()
	if err != nil {
		return "", err
	}
	scores := map[string]float64{}
	for _, e := range entries {
		hits := float64(matchCount(e, terms)) * e.weight()
		if hits == 0 {
			continue
		}
		seen := map[string]bool{}
		for _, p := range e.Paths {
			if !seen[p.Root] {
				seen[p.Root] = true
				scores[p.Root] += hits
			}
		}
	}
	best := ""
	for root, score := range scores {
		if best == "" || score > scores[best] || (score == scores[best] && root < best) {
			best = root
		}
	}
	return best, nil
}

// Halves the weight of the record of a relay whose answer the requester said was wrong
// The record is appended again since the last record of a relay wins
func (m Map) Correct(relayId string, now time.Time) (Entry, error) {
	entries, err := m.entries()
	if err != nil {
		return Entry{}, err
	}
	i := slices.IndexFunc(entries, func(e Entry) bool { return e.RelayId == relayId })
	if relayId == "" || i < 0 {
		return Entry{}, fmt.Errorf("no record of relay %q", relayId)
	}
	e := entries[i]
	e.Weight, e.RecordedAt = e.weight()/2, now.UTC()
	return e, m.Add(e)
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

// A term counts when it is a whole word of the topic or a keyword, or a keyword is a whole word of it
// So svc never matches inside pointsvc while 할당이 still matches the keyword 할당
func matchCount(e Entry, terms []string) int {
	topic := strings.ToLower(e.Topic)
	n := 0
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if wholeWord(topic, t) || keywordMatches(e.Keywords, t) {
			n++
		}
	}
	return n
}

func keywordMatches(keywords []string, term string) bool {
	for _, k := range keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" && (wholeWord(k, term) || wholeWord(term, k)) {
			return true
		}
	}
	return false
}

// Whether word occurs in s with no letter or digit right around it
// Any non ASCII rune may follow so a Korean particle can end the word
func wholeWord(s, word string) bool {
	for i := 0; word != ""; {
		at := strings.Index(s[i:], word)
		if at < 0 {
			return false
		}
		at += i
		end := at + len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:at])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if (at == 0 || !wordRune(before)) && (end == len(s) || after > unicode.MaxASCII || !wordRune(after)) {
			return true
		}
		i = at + 1
	}
	return false
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }

// A map larger than this is compacted
// Below it reading every line costs less than rewriting the file
const compactAt = 256 << 10

// Rewrites the map without lines no lookup uses again and reports whether it wrote
// 1. Only the last record of a relay is kept since entries reads no other
// 2. A record whose paths are all gone is dropped since Locate drops them anyway
// 3. A line that does not parse is dropped since it fails every lookup
func (m Map) Compact() (bool, error) {
	info, err := os.Stat(m.file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case info.Size() <= compactAt:
		return false, nil
	}
	return fileio.RewriteLines(m.file, useful)
}

func useful(lines [][]byte) [][]byte {
	entries := make([]*Entry, len(lines))
	last := map[string]int{}
	for i, l := range lines {
		var e Entry
		if json.Unmarshal(l, &e) != nil {
			continue
		}
		entries[i] = &e
		if e.RelayId != "" {
			last[e.RelayId] = i
		}
	}
	var kept [][]byte
	for i, e := range entries {
		if e == nil || (e.RelayId != "" && last[e.RelayId] != i) || !slices.ContainsFunc(e.Paths, Path.Exists) {
			continue
		}
		kept = append(kept, lines[i])
	}
	return kept
}
