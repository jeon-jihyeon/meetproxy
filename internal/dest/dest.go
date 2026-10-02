// Package dest is the posting destination allow list
package dest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	githubURL = regexp.MustCompile(`^https?://github\.com/([\w.-]+)/([\w.-]+)`)
	slackURL  = regexp.MustCompile(`^https?://[\w-]+\.slack\.com/archives/([A-Z0-9]+)`)
)

// Normalizes links to `github:owner/repo` or `slack:channel` so patterns do not depend on link shape
func Location(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "github:") || strings.HasPrefix(raw, "slack:") {
		return raw, true
	}
	if m := githubURL.FindStringSubmatch(raw); m != nil {
		return "github:" + m[1] + "/" + m[2], true
	}
	if m := slackURL.FindStringSubmatch(raw); m != nil {
		return "slack:" + m[1], true
	}
	return "", false
}

type Allow struct{ file string }

func New(dataDir string) Allow { return Allow{file: filepath.Join(dataDir, "dest.json")} }

type config struct {
	Allow []string `json:"allow"`
}

func (a Allow) Add(pattern string) error {
	ps, err := a.patterns()
	if err != nil {
		return err
	}
	if slices.Contains(ps, pattern) {
		return nil
	}
	b, err := json.MarshalIndent(config{Allow: append(ps, pattern)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(a.file, b, 0o644)
}

// Denies when nothing matches or the input cannot be normalized
func (a Allow) Allowed(raw string) (bool, error) {
	loc, ok := Location(raw)
	if !ok {
		return false, nil
	}
	ps, err := a.patterns()
	if err != nil {
		return false, err
	}
	for _, p := range ps {
		if match(p, loc) {
			return true, nil
		}
	}
	return false, nil
}

func (a Allow) patterns() ([]string, error) {
	b, err := os.ReadFile(a.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c config
	return c.Allow, json.Unmarshal(b, &c)
}

// `*` matches any string
func match(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
