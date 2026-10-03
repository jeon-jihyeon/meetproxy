package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type hookInput struct {
	SessionId string `json:"session_id"`
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		Command   string `json:"command"`
		ChannelId string `json:"channel_id"`
	} `json:"tool_input"`
}

var errUnknownDest = errors.New("posting to an unknown destination, name the repository or channel")

func runHook(data string, args []string, in io.Reader, out io.Writer) error {
	switch strings.Join(args, " ") {
	case "path":
		return observePath(data, in)
	case "guard":
		// Fail closed since this is the safety check
		if err := guardPost(data, in, out); err != nil {
			return deny(out, "meetproxy: posting check failed "+err.Error())
		}
		return nil
	default:
		return fmt.Errorf("unknown hook %v", args)
	}
}

var errNoData = errors.New("CLAUDE_PLUGIN_DATA is not set")

func observePath(data string, in io.Reader) error {
	if data == "" {
		return errNoData
	}
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	target := h.ToolInput.FilePath
	if target == "" {
		target = h.ToolInput.Path
	}
	if target == "" || h.SessionId == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(h.Cwd, target)
	}
	p, ok := locmap.Resolve(target)
	if !ok {
		return nil
	}
	return relay.New(data).Observe(h.SessionId, p)
}

// Enforced by a hook because request text is untrusted and no person reviews the post
// Non posting calls return before any relay state is read so broken state never blocks them
func guardPost(data string, in io.Reader, out io.Writer) error {
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	locs, targetErr := postTargets(h)
	if targetErr == nil && len(locs) == 0 {
		return nil
	}
	if data == "" {
		return errNoData
	}
	if h.SessionId == "" {
		return nil
	}
	r, err := relay.New(data).Current(h.SessionId)
	if errors.Is(err, relay.ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	if targetErr != nil {
		return deny(out, fmt.Sprintf("meetproxy: relay %s is open, %s", r.Id, targetErr))
	}
	allow := dest.New(data)
	for _, loc := range locs {
		allowed, err := allow.Allowed(loc)
		if err != nil {
			return err
		}
		if !allowed {
			return deny(out, fmt.Sprintf(
				"meetproxy: relay %s is open, %s is not allowed. Run meetproxy close if this post is unrelated", r.Id, loc))
		}
	}
	return nil
}

var (
	slackPosts = []string{"send_message", "schedule_message", "create_canvas", "update_canvas"}
	directAPI  = regexp.MustCompile(`slack\.com/api|hooks\.slack\.com|api\.github\.com`)
	ghInText   = regexp.MustCompile(`\bgh\s`)
	repoRef    = regexp.MustCompile(`^(?:https?://github\.com/|/?repos/)([\w.-]+)/([\w.-]+)`)
	wrappers   = map[string]bool{
		"builtin": true, "command": true, "doas": true, "env": true, "exec": true, "nice": true,
		"nohup": true, "stdbuf": true, "sudo": true, "time": true, "timeout": true, "xargs": true,
	}
	ghReads = map[string]bool{
		"pr list": true, "pr view": true, "pr status": true, "pr diff": true, "pr checks": true, "pr checkout": true,
		"issue list": true, "issue view": true, "issue status": true,
		"release list": true, "release view": true, "release download": true,
		"gist list": true, "gist view": true, "gist clone": true,
	}
)

// Returns nothing for a non-posting call and errUnknownDest when the destination cannot be told
func postTargets(h hookInput) ([]string, error) {
	if strings.Contains(strings.ToLower(h.ToolName), "slack") {
		if !slices.ContainsFunc(slackPosts, func(s string) bool { return strings.HasSuffix(h.ToolName, s) }) {
			return nil, nil
		}
		if h.ToolInput.ChannelId == "" {
			return nil, errUnknownDest
		}
		return []string{"slack:" + h.ToolInput.ChannelId}, nil
	}
	if h.ToolName != "Bash" {
		return nil, nil
	}
	return bashTargets(h.ToolInput.Command, 0)
}

// Every repository named in a posting command must pass, not just the first one found
func bashTargets(cmd string, depth int) ([]string, error) {
	if directAPI.MatchString(cmd) {
		return nil, errUnknownDest
	}
	var locs []string
	for _, words := range shellCommands(cmd) {
		ls, err := ghTargets(words)
		if err != nil {
			return nil, err
		}
		locs = append(locs, ls...)
		if depth >= 3 {
			continue
		}
		// Text run by bash -c, eval or a quoted substitution is checked as a command too
		for _, w := range words {
			if !ghInText.MatchString(w) {
				continue
			}
			ls, err := bashTargets(w, depth+1)
			if err != nil {
				return nil, err
			}
			locs = append(locs, ls...)
		}
	}
	return locs, nil
}

// Anything under pr, issue, release or gist that is not a known read counts as a write
func ghTargets(words []string) ([]string, error) {
	i := ghIndex(words)
	if i < 0 {
		return nil, nil
	}
	pre, args := words[:i], words[i+1:]
	if len(args) < 2 {
		return nil, nil
	}
	switch args[0] {
	case "pr", "issue", "release":
		if ghReads[args[0]+" "+args[1]] {
			return nil, nil
		}
	case "gist":
		if ghReads["gist "+args[1]] {
			return nil, nil
		}
		return nil, errUnknownDest
	case "api":
		if !apiWrites(args[1:]) {
			return nil, nil
		}
	default:
		return nil, nil
	}
	locs := repoArgs(pre, args)
	if len(locs) == 0 {
		return nil, errUnknownDest
	}
	return locs, nil
}

// Index of gh as the command name after assignments and wrappers
func ghIndex(words []string) int {
	for i, w := range words {
		switch {
		case filepath.Base(w) == "gh":
			return i
		case wrappers[w], strings.HasPrefix(w, "-"), strings.Contains(w, "="), isNumber(w):
			continue
		default:
			return -1
		}
	}
	return -1
}

func isNumber(w string) bool {
	return strings.Trim(w, "0123456789.smhd") == "" && w != ""
}

func apiWrites(args []string) bool {
	for i, w := range args {
		method := ""
		switch {
		case strings.HasPrefix(w, "--field"), strings.HasPrefix(w, "--raw-field"), strings.HasPrefix(w, "--input"):
			return true
		case strings.HasPrefix(w, "--method="):
			method = strings.TrimPrefix(w, "--method=")
		case w == "--method" || w == "-X":
			if i+1 < len(args) {
				method = args[i+1]
			}
		case strings.HasPrefix(w, "-X"):
			method = strings.TrimPrefix(w, "-X")
		case strings.HasPrefix(w, "-f"), strings.HasPrefix(w, "-F"):
			return true
		}
		if method != "" && !strings.EqualFold(method, "GET") && !strings.EqualFold(method, "HEAD") {
			return true
		}
	}
	return false
}

// Collects GH_REPO, --repo, -R and positional repository links or api paths
// A bare flag takes the next word as its value so a link in a body is never read as the target
func repoArgs(pre, args []string) []string {
	var locs []string
	add := func(v string) {
		parts := strings.Split(strings.Trim(v, "/"), "/")
		if len(parts) >= 2 {
			v = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
		locs = append(locs, "github:"+v)
	}
	for _, w := range pre {
		if v, ok := strings.CutPrefix(w, "GH_REPO="); ok {
			add(v)
		}
	}
	for j := 0; j < len(args); j++ {
		w := args[j]
		switch {
		case w == "--repo" || w == "-R":
			if j+1 < len(args) {
				add(args[j+1])
				j++
			}
		case strings.HasPrefix(w, "--repo="):
			add(strings.TrimPrefix(w, "--repo="))
		case strings.HasPrefix(w, "-R"):
			add(strings.TrimPrefix(w, "-R"))
		case strings.HasPrefix(w, "-"):
			if (len(w) == 2 || strings.HasPrefix(w, "--")) && !strings.Contains(w, "=") {
				j++
			}
		default:
			if m := repoRef.FindStringSubmatch(w); m != nil {
				add(m[1] + "/" + m[2])
			}
		}
	}
	return locs
}

// Splits a shell command into simple commands of words
// 1. quotes and backslashes are removed
// 2. unquoted ; & | ( ) backtick and newline end a command
func shellCommands(s string) [][]string {
	var t tokenizer
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c == '\\' && t.quote != '\'' && i+1 < len(rs) {
			i++
			t.add(rs[i])
			continue
		}
		switch {
		case t.quote != 0:
			if c == t.quote {
				t.quote = 0
			} else {
				t.add(c)
			}
		case c == '\'' || c == '"':
			t.quote, t.inWord = c, true
		case c == ' ' || c == '\t':
			t.endWord()
		case strings.ContainsRune(";&|()`\n", c):
			t.endCommand()
		default:
			t.add(c)
		}
	}
	t.endCommand()
	return t.out
}

type tokenizer struct {
	out    [][]string
	words  []string
	cur    strings.Builder
	quote  rune
	inWord bool
}

func (t *tokenizer) add(c rune) {
	t.cur.WriteRune(c)
	t.inWord = true
}

func (t *tokenizer) endWord() {
	if t.inWord {
		t.words = append(t.words, t.cur.String())
	}
	t.cur.Reset()
	t.inWord = false
}

func (t *tokenizer) endCommand() {
	t.endWord()
	if len(t.words) > 0 {
		t.out = append(t.out, t.words)
	}
	t.words = nil
}

func deny(out io.Writer, reason string) error {
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}
