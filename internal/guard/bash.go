package guard

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

var (
	directAPI = regexp.MustCompile(`(?i)slack\.com/api|hooks\.slack\.com|(?:api|uploads)\.github\.com|/api/v3/|/api/graphql`)
	ghMention = regexp.MustCompile(`\bgh\b`)
	// Turns the commands inside substitutions into commands of their own when the word is read again
	openers = strings.NewReplacer("$(", "\n", "`", "\n", "<(", "\n", ">(", "\n")
	// Commands that run the command a later word names
	wrappers = map[string]bool{
		"env": true, "command": true, "exec": true, "sudo": true, "nohup": true, "time": true, "nice": true,
		"timeout": true, "caffeinate": true, "watch": true, "stdbuf": true, "xargs": true, "parallel": true,
	}
	// Wrappers that add words read from their input to the command they run
	feeders = map[string]bool{"xargs": true, "parallel": true}
	shells  = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}
	// Interpreters whose inline program the guard cannot read
	interpreter = regexp.MustCompile(`^(?:python[\d.]*|node|ruby|perl|osascript)$`)
	// Words that stand for themselves although they hold a character expansion uses
	plainWords = map[string]bool{"{": true, "}": true, "[": true, "[[": true}
	// meetproxy commands that change what is taken or where posts may go
	settings = map[string]bool{
		"allow": true, "resume": true, "delegation put": true, "delegation remove": true,
		"inbox take": true, "inbox claim": true, "inbox add": true, "inbox advance": true,
		"slack token": true,
	}
	// meetproxy flags that take no value
	meetproxyBools = map[string]bool{"busy": true}
)

// Text inside text deeper than this that still names gh is denied
const maxDepth = 8

func bash(text string) ([]Post, error) {
	if directAPI.MatchString(text) {
		return nil, unknown("a direct API call")
	}
	var s scan
	if err := s.text(text, 0); err != nil {
		return nil, err
	}
	return s.posts, nil
}

type scan struct {
	posts []Post
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
// 1. The program of a shell -c or eval is always read
// 2. The text a gh flag such as --body takes is never run so only its substitutions are read
func nested(c shell.Command) []string {
	gh := slices.IndexFunc(c.Args, isGh)
	prog := program(c.Args)
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
		if i >= prog {
			out = append(out, openers.Replace(w))
			continue
		}
		add(w, gh < 0 || i <= gh || !ghText[c.Args[i-1]])
	}
	return out
}

// Index of the first word run as shell text or the length of args when none is
func program(args []string) int {
	for i, w := range args {
		if base(w) == "eval" {
			return i + 1
		}
		if !shells[base(w)] {
			continue
		}
		for j := i + 1; j < len(args); j++ {
			if shortFlag(args[j], "c") {
				return j + 1
			}
		}
	}
	return len(args)
}

func base(w string) string { return filepath.Base(w) }

func isGh(w string) bool        { return base(w) == "gh" }
func isMeetproxy(w string) bool { return base(w) == "meetproxy" }
func isFeeder(w string) bool    { return feeders[base(w)] }

// The command name holds an expansion, a glob or a brace so the program it runs cannot be told
func expanded(w string) bool {
	b := base(w)
	return !plainWords[b] && strings.ContainsAny(b, "$`{*?[")
}

// A cluster of short flags such as -lc that holds one of letters
func shortFlag(w, letters string) bool {
	return len(w) > 1 && w[0] == '-' && w[1] != '-' && strings.ContainsAny(w[1:], letters)
}

func inline(w string) bool {
	return shortFlag(w, "cep") || w == "--eval" || w == "--print"
}

// Every word whose base name is gh or meetproxy starts a call whatever wrapper comes before it
func (s *scan) command(c shell.Command) error {
	if len(c.Args) == 0 {
		return nil
	}
	if err := opaque(c); err != nil {
		return err
	}
	if i := slices.IndexFunc(c.Args, isMeetproxy); i >= 0 {
		posts, err := meetproxyCall(c.Args[i+1:])
		s.posts = append(s.posts, posts...)
		if err != nil {
			return err
		}
	}
	i := slices.IndexFunc(c.Args, isGh)
	if i < 0 {
		return nil
	}
	posts, err := ghCall(c.Args[i+1:], envRepos(c.Env, c.Args[:i]), slices.ContainsFunc(c.Args[:i], isFeeder))
	s.posts = append(s.posts, posts...)
	return err
}

// Commands whose program the guard cannot read
// 1. A word that names a direct API host even when quotes split it
// 2. A command named by an expansion, a glob or a brace, also behind a wrapper
// 3. An interpreter given its program inline
// 4. A shell reading its program from a pipe
func opaque(c shell.Command) error {
	if slices.ContainsFunc(slices.Concat(c.Args, slices.Collect(maps.Values(c.Env))), directAPI.MatchString) {
		return unknown("a direct API call")
	}
	name := c.Args[0]
	if expanded(name) || (wrappers[base(name)] && slices.ContainsFunc(c.Args[1:], expanded)) {
		return unknown("a command named by an expansion")
	}
	if i := slices.IndexFunc(c.Args, isInterpreter); i >= 0 && slices.ContainsFunc(c.Args[i+1:], inline) {
		return unknown("an inline " + base(c.Args[i]) + " program")
	}
	if i := commandName(c.Args); i >= 0 && shells[base(c.Args[i])] && c.Stdin == "" && !slices.ContainsFunc(c.Args[i+1:], readsProgram) {
		return unknown("a shell reading its program from a pipe")
	}
	return nil
}

func isInterpreter(w string) bool { return interpreter.MatchString(base(w)) }

// A shell word that names its program as a file or with -c
func readsProgram(w string) bool { return !strings.HasPrefix(w, "-") || shortFlag(w, "c") }

// Index of the first word past wrappers, their options and assignments or -1
func commandName(args []string) int {
	return slices.IndexFunc(args, func(w string) bool {
		return !wrappers[base(w)] && !strings.HasPrefix(w, "-") && !strings.Contains(w, "=") && strings.Trim(w, "0123456789") != ""
	})
}

// What a meetproxy command does while a request is handled
// 1. Commands that change what is taken or where posts may go are left to the user
// 2. open posts to the origin and the target it names so the relay never moves to another request
// 3. slack post posts to its link
func meetproxyCall(args []string) ([]Post, error) {
	pos, target := meetproxyWords(args)
	word := func(i int) string {
		if i < len(pos) {
			return pos[i]
		}
		return ""
	}
	first, second := word(0), word(1)
	switch {
	case settings[first] || settings[first+" "+second] || (first == "triage" && second != ""):
		return nil, fmt.Errorf("meetproxy %s %w", strings.TrimSpace(first+" "+second), ErrSettings)
	case first == "slack" && second == "post":
		loc, ok := dest.Parse(word(2))
		if !ok {
			return nil, unknown("meetproxy slack post of a link that names no place")
		}
		return []Post{{At: loc}}, nil
	case first != "open" || second == "":
		return nil, nil
	}
	links := []string{second}
	if target != "" {
		links = append(links, target)
	}
	var posts []Post
	for _, link := range links {
		loc, ok := dest.Parse(link)
		if !ok {
			return nil, unknown("meetproxy open of a link that names no place")
		}
		posts = append(posts, Post{At: loc})
	}
	return posts, nil
}

// Positional words and the --target value of a meetproxy command line
// Every meetproxy flag but the boolean ones takes a value
func meetproxyWords(args []string) (pos []string, target string) {
	for i := 0; i < len(args); i++ {
		w := args[i]
		switch {
		case w == "--":
			return append(pos, args[i+1:]...), target
		case strings.HasPrefix(w, "-") && strings.Contains(w, "="):
			name, v, _ := strings.Cut(w, "=")
			if strings.TrimLeft(name, "-") == "target" {
				target = v
			}
		case strings.HasPrefix(w, "-") && meetproxyBools[strings.TrimLeft(w, "-")]:
		case strings.HasPrefix(w, "-") && len(w) > 1:
			if i+1 < len(args) && strings.TrimLeft(w, "-") == "target" {
				target = args[i+1]
			}
			i++
		default:
			pos = append(pos, w)
		}
	}
	return pos, target
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
