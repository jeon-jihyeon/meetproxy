// Package dest is the posting destination allow list
package dest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

const (
	GitHub = "github"
	Slack  = "slack"
)

// Where a post goes written as source:name and an optional #number
type Location struct {
	Source string
	// Slack channel id or GitHub owner and repository joined by a slash
	Name string
	// Pull request or issue number and zero for the whole repository or channel
	Number int
}

func (l Location) String() string {
	s := l.Source + ":" + l.Name
	if l.Number > 0 {
		s += "#" + strconv.Itoa(l.Number)
	}
	return s
}

// Whether a post to p goes where l is
// A location with a number covers only that pull request or issue
func (l Location) Covers(p Location) bool {
	return l.Source == p.Source && l.Name == p.Name && (l.Number == 0 || l.Number == p.Number)
}

// Adding a source is one entry here
type source struct {
	name string
	// Link whose first group is the name and optional second group the number
	link *regexp.Regexp
	// Name in the written form
	written *regexp.Regexp
	// Name of an allow pattern where * matches any text
	pattern *regexp.Regexp
}

var sources = []source{
	{
		GitHub,
		regexp.MustCompile(`^https?://github\.com/([\w.-]+/[\w.-]+)(?:/(?:pull|issues)/(\d+))?`),
		regexp.MustCompile(`^[\w.-]+/[\w.-]+$`),
		regexp.MustCompile(`^(?:\*|[\w.*-]+/[\w.*-]+)$`),
	},
	{
		Slack,
		regexp.MustCompile(`^https?://[\w-]+\.slack\.com/archives/([A-Z0-9]+)`),
		regexp.MustCompile(`^[A-Z0-9]+$`),
		regexp.MustCompile(`^[A-Z0-9*]+$`),
	},
}

// Reads a link or the written form so patterns do not depend on link shape
func Parse(raw string) (Location, bool) {
	raw = strings.TrimSpace(raw)
	for _, s := range sources {
		if m := s.link.FindStringSubmatch(raw); m != nil {
			n, _ := strconv.Atoi(m[len(m)-1])
			return Location{Source: s.name, Name: m[1], Number: n}, true
		}
		rest, ok := strings.CutPrefix(raw, s.name+":")
		if !ok {
			continue
		}
		name, num, numbered := strings.Cut(rest, "#")
		n, err := strconv.Atoi(num)
		if !s.written.MatchString(name) || (numbered && (err != nil || n <= 0)) {
			return Location{}, false
		}
		return Location{Source: s.name, Name: name, Number: n}, true
	}
	return Location{}, false
}

type Allow struct{ file string }

func New(dataDir string) Allow { return Allow{file: filepath.Join(dataDir, "dest.json")} }

type config struct {
	Allow []string `json:"allow"`
}

// Holds a lock so concurrent adds never drop each other
func (a Allow) Add(pattern string) error {
	if !validPattern(pattern) {
		return fmt.Errorf("pattern must look like github:owner/repo or slack:CHANNEL %q", pattern)
	}
	unlock, err := fileio.Lock(a.file + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := a.Patterns()
	if err != nil {
		return err
	}
	if slices.Contains(ps, pattern) {
		return nil
	}
	return fileio.WriteJSON(a.file, config{Allow: append(ps, pattern)})
}

// A pattern that covers a whole source such as slack:* would let a request text post anywhere so it is refused
func validPattern(p string) bool {
	for _, s := range sources {
		if name, ok := strings.CutPrefix(p, s.name+":"); ok {
			return s.pattern.MatchString(name) && strings.Trim(name, "*/") != ""
		}
	}
	return false
}

// A pattern names a repository or channel so it covers every number in it
func (a Allow) Allowed(loc Location) (bool, error) {
	ps, err := a.Patterns()
	if err != nil {
		return false, err
	}
	key := Location{Source: loc.Source, Name: loc.Name}.String()
	for _, p := range ps {
		if match(p, key) {
			return true, nil
		}
	}
	return false, nil
}

func (a Allow) Patterns() ([]string, error) {
	var c config
	_, err := fileio.ReadJSON(a.file, &c)
	return c.Allow, err
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
