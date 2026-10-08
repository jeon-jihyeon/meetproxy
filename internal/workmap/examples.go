package workmap

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/shell"
)

// An output a session sent and its kind
type output struct {
	kind Kind
	Example
}

// The text of one output a tool use sends
type draft struct {
	kind Kind
	text string
}

// The outputs a tool use sent at
// 1. Bash: git commits and branches and gh pull requests, reviews, comments and issues
// 2. MCP tools that post to Slack, Linear, mail, sheets and documents and the meetproxy post tool
func written(name string, input json.RawMessage, at time.Time) []draft {
	if name == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) != nil {
			return nil
		}
		var out []draft
		cmds, _ := shell.Commands(in.Command)
		for i, c := range cmds {
			files := messageFiles{stdin: c.Stdin, ranAt: at}
			// A lone cat of a here document is how a session pipes a message to the next command
			if c.Stdin == "" && i > 0 && slices.Equal(cmds[i-1].Args, []string{"cat"}) {
				files.stdin = cmds[i-1].Stdin
			}
			if d := sent(c.Args, files); d.kind != "" && strings.TrimSpace(d.text) != "" {
				out = append(out, draft{d.kind, strings.TrimSpace(d.text)})
			}
		}
		return out
	}
	if !strings.HasPrefix(name, "mcp__") {
		return nil
	}
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	kind := toolKind(strings.ToLower(name), in)
	text := paragraphs(field(in, "title", "subject"), field(in, "body", "description", "text", "message", "textContent", "markdown", "values"))
	if kind == "" || text == "" {
		return nil
	}
	return []draft{{kind, text}}
}

// The kind of output an MCP tool sends by the words in its lower case name
func toolKind(name string, in map[string]any) Kind {
	switch {
	case strings.Contains(name, "meetproxy") && strings.HasSuffix(name, "__post"):
		link, _ := in["link"].(string)
		return linkKind(link)
	case strings.Contains(name, "drive") && strings.Contains(name, "create_file"):
		// A text upload becomes a Google Doc unless its type is a spreadsheet
		mime, _ := in["contentMimeType"].(string)
		if strings.Contains(mime, "spreadsheet") || strings.Contains(mime, "csv") {
			return KindSheet
		}
		return KindDocument
	}
	for _, t := range toolKinds {
		named := true
		for _, words := range t.groups {
			named = named && slices.ContainsFunc(words, func(w string) bool { return strings.Contains(name, w) })
		}
		if named {
			return t.kind
		}
	}
	return ""
}

var writes = []string{"create", "update", "append", "write", "insert"}

// The words a tool name holds for each kind
// The name holds one word of every group
var toolKinds = []struct {
	kind   Kind
	groups [][]string
}{
	{KindSlackMessage, [][]string{{"slack"}, {"send_message"}}},
	{KindLinearIssue, [][]string{{"linear"}, {"save_issue", "create_issue"}}},
	{KindEmail, [][]string{{"mail"}, {"create_draft", "send"}}},
	{KindSheet, [][]string{{"sheet"}, writes}},
	{KindDocument, [][]string{{"confluence"}, {"create", "update"}, {"page"}}},
	{KindDocument, [][]string{{"notion"}, writes}},
	{KindDocument, [][]string{{"google"}, {"docs"}, writes}},
}

// A Slack link is a message and a GitHub link a pull request comment or an issue comment
func linkKind(link string) Kind {
	switch {
	case strings.Contains(link, "slack.com"):
		return KindSlackMessage
	case strings.Contains(link, "github.com") && strings.Contains(link, "/pull/"):
		return KindReviewComment
	case strings.Contains(link, "github.com") && strings.Contains(link, "/issues/"):
		return KindIssue
	}
	return ""
}

// The first key of in with a value
// A value other than a string such as the rows of a sheet is kept as JSON
func field(in map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := in[k].(type) {
		case nil:
		case string:
			if v != "" {
				return v
			}
		default:
			b, _ := json.Marshal(v)
			return string(b)
		}
	}
	return ""
}

func paragraphs(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// Interpreters a script runs under that wraps git such as a commit hook
var interpreters = []string{"python", "python3", "bash", "sh", "zsh", "node", "ruby", "perl"}

// The output one simple command sends
// 1. git and gh are found after any wrapper such as env, sudo or timeout
// 2. A script run by an interpreter and given a commit subcommand wraps git commit
func sent(args []string, files messageFiles) draft {
	i := slices.IndexFunc(args, func(a string) bool { return filepath.Base(a) == "git" || filepath.Base(a) == "gh" })
	switch {
	case i >= 0 && filepath.Base(args[i]) == "git":
		return gitOutput(args[i+1:], files)
	case i >= 0:
		return ghOutput(args[i+1:], files)
	case len(args) > 0 && slices.Contains(interpreters, filepath.Base(args[0])) && slices.Contains(args, "commit"):
		return gitOutput(args[slices.Index(args, "commit"):], files)
	}
	return draft{}
}

// A commit message from every -m and from a file read on stdin
// A branch name from checkout -b, switch -c and worktree add -b
func gitOutput(args []string, files messageFiles) draft {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-C" || args[i] == "-c" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return draft{}
	}
	sub, args := args[i], args[i+1:]
	switch sub {
	case "commit":
		return draft{KindCommit, commitMessage(args, files)}
	case "checkout", "switch", "worktree":
		return draft{KindBranch, flag(args, "-b", "-c", "-B", "-C")}
	}
	return draft{}
}

// Every -m paragraph and the text of every message file
func commitMessage(args []string, files messageFiles) string {
	var msgs []string
	for j := 0; j < len(args); j++ {
		name, value, next := commitFlag(args[j])
		if next && j+1 < len(args) {
			j++
			value = args[j]
		}
		switch name {
		case 'm':
			msgs = append(msgs, value)
		case 'F':
			msgs = append(msgs, files.text(value))
		}
	}
	return paragraphs(msgs...)
}

// The flag of git commit a word sets and its value
// 1. --message and --file are m and F with the value after an equals sign or in the next word
// 2. In a cluster of short flags such as -am or -mtext a flag that takes a value takes the rest of the word or else the next word
func commitFlag(a string) (name byte, value string, next bool) {
	switch {
	case a == "--message":
		return 'm', "", true
	case a == "--file":
		return 'F', "", true
	case strings.HasPrefix(a, "--message="):
		return 'm', strings.TrimPrefix(a, "--message="), false
	case strings.HasPrefix(a, "--file="):
		return 'F', strings.TrimPrefix(a, "--file="), false
	}
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return 0, "", false
	}
	for i := 1; i < len(a); i++ {
		if strings.IndexByte("mFcCt", a[i]) < 0 {
			continue
		}
		if i+1 < len(a) {
			return a[i], a[i+1:], false
		}
		return a[i], "", true
	}
	return 0, "", false
}

// The stdin of a command and when it ran which together say what a message file flag reads
type messageFiles struct {
	// Here document or here-string the command reads
	stdin string
	ranAt time.Time
}

// A file written later than this after the command ran holds a later message
const fileSlack = time.Minute

// The text a message file flag names
// 1. `-` and /dev/stdin are the stdin of the command
// 2. An absolute path is read while it is still there since sessions often write the body to a scratch file
// 3. Only a regular file written before the command ran is read so a reused path never gives a later text
// 4. Opening never blocks so a pipe or a device path cannot stall a refresh
func (m messageFiles) text(path string) string {
	if path == "-" || path == "/dev/stdin" {
		return m.stdin
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.ModTime().After(m.ranAt.Add(fileSlack)) {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4*exampleRunes))
	return string(b)
}

// gh takes the repository flag before its command
func ghOutput(args []string, files messageFiles) draft {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-R" || args[i] == "--repo" {
			i++
		}
		i++
	}
	if len(args)-i < 2 {
		return draft{}
	}
	sub, verb, args := args[i], args[i+1], args[i+2:]
	if sub == "api" {
		if pullComments(append([]string{verb}, args...)) {
			return draft{KindReviewComment, apiBody(append([]string{verb}, args...), files)}
		}
		return draft{}
	}
	body := flag(args, "--body", "-b")
	if file := flag(args, "--body-file", "-F"); file != "" {
		body = files.text(file)
	}
	title := flag(args, "--title", "-t")
	switch sub + " " + verb {
	case "pr create":
		return draft{KindPullRequest, paragraphs(title, body)}
	case "pr comment", "pr review":
		return draft{KindReviewComment, body}
	case "issue create":
		return draft{KindIssue, paragraphs(title, body)}
	case "issue comment":
		return draft{KindIssue, body}
	}
	return draft{}
}

func pullComments(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		return strings.Contains(a, "/pulls/") && (strings.Contains(a, "/comments") || strings.Contains(a, "/reviews"))
	})
}

// The body field of a gh api call from a field flag or from JSON of the input file
func apiBody(args []string, files messageFiles) string {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-f", "-F", "--field", "--raw-field":
			k, v, ok := strings.Cut(args[i+1], "=")
			if !ok || k != "body" {
				continue
			}
			// gh reads a typed field value that starts with @ from that file
			if file, ok := strings.CutPrefix(v, "@"); ok && args[i] != "-f" && args[i] != "--raw-field" {
				return files.text(file)
			}
			return v
		}
	}
	var in struct {
		Body string `json:"body"`
	}
	if input := flag(args, "--input"); input != "" && json.Unmarshal([]byte(files.text(input)), &in) == nil {
		return in.Body
	}
	return ""
}

// The value of the first of names in args as the next word or for a long name after an equals sign
func flag(args []string, names ...string) string {
	for i, a := range args {
		if slices.Contains(names, a) && i+1 < len(args) {
			return args[i+1]
		}
		for _, n := range names {
			if v, ok := strings.CutPrefix(a, n+"="); ok && strings.HasPrefix(n, "--") {
				return v
			}
		}
	}
	return ""
}
