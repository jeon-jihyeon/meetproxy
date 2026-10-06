package workmap

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
)

// The part of a transcript line a case and an example need
type record struct {
	Type             string    `json:"type"`
	IsMeta           bool      `json:"isMeta"`
	IsCompactSummary bool      `json:"isCompactSummary"`
	PromptSource     string    `json:"promptSource"`
	Cwd              string    `json:"cwd"`
	Timestamp        time.Time `json:"timestamp"`
	AttributionSkill string    `json:"attributionSkill"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type toolUse struct {
	Type  string          `json:"type"`
	Id    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResult struct {
	Type      string `json:"type"`
	ToolUseId string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
}

var command = regexp.MustCompile(`(?s)<command-name>/?([^<]+)</command-name>.*?<command-args>(.*?)</command-args>`)

// Built-in commands say nothing about how a request is answered
var builtins = map[string]bool{"clear": true, "compact": true, "resume": true, "model": true, "config": true, "help": true, "exit": true, "login": true, "reload-plugins": true, "plugin": true, "mcp": true, "loop": true}

// A typed prompt opens a case and the tool uses until the next one fill it
type caseBuilder struct {
	source string
	out    []Case
	open   *Case
	// The first working directory of the transcript outside temporary folders
	home   string
	skills map[string]int
	files  map[string]int
	// Outputs by the id of the tool use that sent them until its result says it worked
	pending map[string][]output
	outputs []output
}

// Reads the complete lines from offset and returns where they end
func (b *caseBuilder) read(file string, offset int64) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return offset, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	end := offset
	for {
		line, err := r.ReadBytes('\n')
		// A line still being written waits for the next refresh
		if err == io.EOF {
			return end, nil
		}
		if err != nil {
			return end, err
		}
		b.add(line, end)
		end += int64(len(line))
	}
}

func (b *caseBuilder) add(line []byte, offset int64) {
	var r record
	if json.Unmarshal(line, &r) != nil {
		return
	}
	if r.Cwd != "" {
		b.placeOf(r.Cwd)
	}
	switch r.Type {
	case "user":
		if prompt, skill, ok := typed(r); ok {
			b.start(prompt, r.Cwd, r.Timestamp, offset)
			if skill != "" && b.open != nil {
				b.skills[skill]++
			}
			return
		}
		b.settle(r)
	case "assistant":
		if b.open != nil && r.AttributionSkill != "" {
			b.skills[r.AttributionSkill]++
		}
		var uses []toolUse
		if json.Unmarshal(r.Message.Content, &uses) != nil {
			return
		}
		for _, u := range uses {
			if u.Type != "tool_use" {
				continue
			}
			// Sessions meetproxy starts have no typed prompt but still send outputs
			for _, d := range written(u.Name, u.Input, r.Timestamp) {
				e := Example{Text: cut(d.text, exampleRunes), At: r.Timestamp.UTC(), Root: b.rootOf(r.Cwd), Source: b.source, Offset: offset}
				b.pending[u.Id] = append(b.pending[u.Id], output{d.kind, e})
			}
			if b.open != nil {
				b.use(u)
			}
		}
	}
}

// Keeps the outputs whose tool use the results of r say worked
func (b *caseBuilder) settle(r record) {
	var results []toolResult
	if json.Unmarshal(r.Message.Content, &results) != nil {
		return
	}
	for _, res := range results {
		outs, ok := b.pending[res.ToolUseId]
		if res.Type != "tool_result" || !ok {
			continue
		}
		delete(b.pending, res.ToolUseId)
		if !res.IsError {
			b.outputs = append(b.outputs, outs...)
		}
	}
}

func (b *caseBuilder) start(prompt, cwd string, at time.Time, offset int64) {
	b.finish()
	root := b.rootOf(cwd)
	if root == "" {
		return
	}
	prompt = clip(prompt, 500)
	b.open = &Case{Prompt: prompt, Tokens: tokens(prompt), Root: root, At: at.UTC(), Source: b.source, Offset: offset}
	b.skills, b.files = map[string]int{}, map[string]int{}
}

// The repository of the place cwd belongs to or that place itself outside any
func (b *caseBuilder) rootOf(cwd string) string {
	root := b.placeOf(cwd)
	if root == "" {
		return ""
	}
	if p, ok := locmap.Resolve(root); ok {
		return p.Root
	}
	return root
}

// A session working in a scratch folder still belongs to the place it started in
func (b *caseBuilder) placeOf(cwd string) string {
	if !temporary(cwd) {
		if b.home == "" {
			b.home = cwd
		}
		return cwd
	}
	return b.home
}

// Claude Code gives each session a scratchpad folder that says nothing about the work
func temporary(dir string) bool {
	return strings.Contains(dir+"/", "/scratchpad/")
}

func (b *caseBuilder) use(u toolUse) {
	var in struct {
		Skill    string `json:"skill"`
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	_ = json.Unmarshal(u.Input, &in)
	if u.Name == "Skill" && in.Skill != "" {
		b.skills[in.Skill]++
	}
	path := cmp.Or(in.FilePath, in.Path)
	if path != "" && filepath.IsAbs(path) {
		b.files[path]++
	}
}

func (b *caseBuilder) finish() {
	if b.open == nil {
		return
	}
	b.open.Skills, b.open.Files = top(b.skills, 5), top(b.files, 8)
	b.out = append(b.out, *b.open)
	b.open = nil
}

// The offset of the earliest tool use whose result is not read yet
func (b *caseBuilder) waiting() (int64, bool) {
	var at int64
	found := false
	for _, outs := range b.pending {
		for _, o := range outs {
			if !found || o.Offset < at {
				at, found = o.Offset, true
			}
		}
	}
	return at, found
}

func (b *caseBuilder) done() []Case {
	b.finish()
	return b.out
}

// A prompt the user typed and no skill
// For a slash command its arguments and its name as the skill
// Prompts the harness or another session added and built-in commands are not the user's requests
func typed(r record) (prompt, skill string, ok bool) {
	// A compaction summary sits where a prompt would but the user never typed it
	if r.IsMeta || r.IsCompactSummary || (r.PromptSource != "" && r.PromptSource != "typed") {
		return "", "", false
	}
	var s string
	if json.Unmarshal(r.Message.Content, &s) != nil {
		return "", "", false
	}
	s = strings.TrimSpace(s)
	if m := command.FindStringSubmatch(s); m != nil {
		name := strings.TrimSpace(m[1])
		// meetproxy hands requests to sessions through its own commands
		// Those are not requests the user typed
		if builtins[name] || strings.HasPrefix(name, "meetproxy:") || strings.TrimSpace(m[2]) == "" {
			return "", "", false
		}
		return strings.TrimSpace(m[2]), name, true
	}
	if s == "" || strings.HasPrefix(s, "<") || len([]rune(s)) < 4 {
		return "", "", false
	}
	return s, "", true
}
