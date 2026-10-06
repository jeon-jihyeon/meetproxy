package guard

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

var (
	directAPI = regexp.MustCompile(`slack\.com/api|hooks\.slack\.com|api\.github\.com`)
	ghMention = regexp.MustCompile(`\bgh\b`)
	// Turns the commands inside substitutions into commands of their own when the word is read again
	openers = strings.NewReplacer("$(", "\n", "`", "\n", "<(", "\n", ">(", "\n")
)

// Text inside text deeper than this that still names gh is denied
const maxDepth = 8

func bash(text string) ([]dest.Location, error) {
	if directAPI.MatchString(text) {
		return nil, unknown("a direct API call")
	}
	s := scan{named: ghMention.MatchString(text)}
	if err := s.text(text, 0); err != nil {
		return nil, err
	}
	return s.locs, nil
}

type scan struct {
	// The whole text names gh somewhere
	named bool
	locs  []dest.Location
}

// Commands run inside a word are read as commands too
// 1. Substitutions and backticks run their text
// 2. A word that names gh among blanks or separators may be run by bash -c, eval or ssh
func (s *scan) text(text string, depth int) error {
	if depth > maxDepth {
		if ghMention.MatchString(text) {
			return unknown("gh nested too deep to read")
		}
		return nil
	}
	for _, c := range shell.Commands(text) {
		if err := s.command(c); err != nil {
			return err
		}
		for _, inner := range nested(c) {
			if err := s.text(inner, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Texts to read again from the words of c
// The text a gh flag such as --body takes is never run so only its substitutions are read
func nested(c shell.Command) []string {
	gh := slices.IndexFunc(c.Args, isGh)
	var out []string
	add := func(w string, prose bool) {
		r := openers.Replace(w)
		if r != w || (prose && ghMention.MatchString(w) && strings.ContainsAny(w, " \t\n;&|()<>")) {
			out = append(out, r)
		}
	}
	add(c.Stdin, true)
	for _, v := range c.Env {
		add(v, true)
	}
	for i, w := range c.Args {
		add(w, gh < 0 || i <= gh || !ghText[c.Args[i-1]])
	}
	return out
}

func isGh(w string) bool { return filepath.Base(w) == "gh" }

// Any word whose base name is gh starts a gh call whatever wrapper comes before it
// A command named by an expansion may be gh when the text names gh
func (s *scan) command(c shell.Command) error {
	if len(c.Args) == 0 {
		return nil
	}
	if s.named && strings.HasPrefix(c.Args[0], "$") {
		return unknown("a command named by an expansion " + c.Args[0])
	}
	i := slices.IndexFunc(c.Args, isGh)
	if i < 0 {
		return nil
	}
	locs, err := ghCall(c.Args[i+1:], envRepos(c.Env, c.Args[:i]))
	s.locs = append(s.locs, locs...)
	return err
}

// GH_REPO set before gh or through a wrapper such as env
func envRepos(env map[string]string, pre []string) []string {
	var out []string
	if v, ok := env["GH_REPO"]; ok {
		out = append(out, v)
	}
	for _, w := range pre {
		if v, ok := strings.CutPrefix(w, "GH_REPO="); ok {
			out = append(out, v)
		}
	}
	return out
}
