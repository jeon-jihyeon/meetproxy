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
}

type Candidate struct {
	Path
	Score  int
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

// Root of the place where most similar answers were found
// 1. Each answer counts once per place however many files it rests on
// 2. Ties go to the root that sorts first and no match returns an empty root
func (m Map) Route(terms []string) (string, error) {
	entries, err := m.entries()
	if err != nil {
		return "", err
	}
	scores := map[string]int{}
	for _, e := range entries {
		hits := matchCount(e, terms)
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
