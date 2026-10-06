package workmap

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// A kind of output the user writes
type Kind string

const (
	KindCommit        Kind = "commit"         // git commit message
	KindPullRequest   Kind = "pull-request"   // title and description of a pull request
	KindReviewComment Kind = "review-comment" // pull request review and comment
	KindIssue         Kind = "issue"          // GitHub issue and issue comment
	KindLinearIssue   Kind = "linear-issue"   // Linear issue title and body
	KindSlackMessage  Kind = "slack-message"  // Slack message and thread reply
	KindEmail         Kind = "email"          // mail and mail draft
	KindSheet         Kind = "sheet"          // spreadsheet
	KindDocument      Kind = "document"       // Confluence, Google Docs and Notion page
	KindBranch        Kind = "branch"         // git branch name
)

var Kinds = []Kind{
	KindCommit, KindPullRequest, KindReviewComment, KindIssue, KindLinearIssue,
	KindSlackMessage, KindEmail, KindSheet, KindDocument, KindBranch,
}

// Words that name each kind in a heading, a front matter or a skill
// 1. An English keyword matches at the start of a word so pr never matches prompt
// 2. A Korean keyword matches anywhere since particles attach to the word
var keywords = map[Kind][]string{
	KindCommit:        {"commit", "커밋"},
	KindPullRequest:   {"pull request", "pull-request", "pr description", "pr body", "pr title", "pr 설명", "pr 본문"},
	KindReviewComment: {"review repl", "review comment", "pr comment", "리뷰 답변", "리뷰 코멘트", "리뷰 댓글"},
	KindIssue:         {"github issue", "gh issue", "issue comment", "깃허브 이슈", "이슈 댓글"},
	KindLinearIssue:   {"linear", "issue body", "리니어"},
	KindSlackMessage:  {"slack", "슬랙"},
	KindEmail:         {"email", "e-mail", "mail", "이메일", "메일"},
	KindSheet:         {"sheet", "spreadsheet", "시트"},
	KindDocument:      {"document", "docs", "confluence", "notion", "google doc", "문서", "컨플루언스", "노션"},
	KindBranch:        {"branch", "브랜치"},
}

// Phrases by which a skill says it writes that output
// A bare word such as branch or slack is too often only context
var describedOutputs = map[Kind][]string{
	KindCommit:        {"commit message", "커밋 메시지"},
	KindPullRequest:   {"pr description", "pull request description", "pr body", "pr 설명"},
	KindReviewComment: {"review repl", "review comment", "리뷰 답변"},
	KindIssue:         {"github issue", "issue comment"},
	KindLinearIssue:   {"linear issue", "리니어 이슈"},
	KindSlackMessage:  {"slack message", "slack notice", "slack announcement", "slack post", "slack reply", "슬랙 메시지", "슬랙 공지"},
	KindEmail:         {"email", "이메일"},
	KindSheet:         {"spreadsheet", "google sheet", "시트"},
	KindDocument:      {"confluence page", "google doc", "notion page", "technical doc"},
	KindBranch:        {"branch name", "브랜치명", "브랜치 이름"},
}

var ErrUnknownKind = errors.New("unknown output kind")

// How the user wants one kind of output written
type Format struct {
	Kind     Kind      `json:"kind"`
	Guides   []Guide   `json:"guides,omitempty"`
	Skills   []Method  `json:"skills,omitempty"`
	Examples []Example `json:"examples,omitempty"`
}

// A section of an instruction file whose heading names a kind
// Only where it is so the session reads the rules as they are now
type Guide struct {
	File    string `json:"file"`
	Heading string `json:"heading"`
	Line    int    `json:"line"`
	// Set for one that only applies in the place of this root
	Root string `json:"root,omitempty"`
}

// An output the user sent from a session
type Example struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	Root string    `json:"root,omitempty"`
	// Transcript and byte offset of the tool use that sent it
	Source string `json:"source"`
	Offset int64  `json:"offset"`
}

const (
	examplesPerKind = 3
	exampleRunes    = 1500
)

func (s Store) Formats() ([]Format, error) {
	var out []Format
	_, err := fileio.ReadJSON(filepath.Join(s.dir, "formats.json"), &out)
	return out, err
}

// The format of kind or an empty one when the map knows nothing of it
func (s Store) Format(kind string) (Format, error) {
	if !slices.Contains(Kinds, Kind(kind)) {
		return Format{}, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	all, err := s.Formats()
	if err != nil {
		return Format{}, err
	}
	for _, f := range all {
		if f.Kind == Kind(kind) {
			return f, nil
		}
	}
	return Format{Kind: Kind(kind)}, nil
}

// The format with only the guides and skills that apply in the place of root
// Every one applies when root is empty
func (f Format) In(root string) Format {
	if root == "" {
		return f
	}
	elsewhere := func(r string) bool { return r != "" && r != root }
	f.Guides = slices.DeleteFunc(slices.Clone(f.Guides), func(g Guide) bool { return elsewhere(g.Root) })
	f.Skills = slices.DeleteFunc(slices.Clone(f.Skills), func(m Method) bool { return elsewhere(m.Root) })
	return f
}

// Guides and skills found again and the newest examples of the old formats and of the reads
func formatsOf(config string, places []Place, methods []Method, old []Format, reads []read) []Format {
	guides := scanGuides(config, places)
	from := rereads(reads)
	examples := map[Kind][]Example{}
	for _, f := range old {
		for _, e := range f.Examples {
			if !covered(from, e.Source, e.Offset) {
				examples[f.Kind] = append(examples[f.Kind], e)
			}
		}
	}
	for _, r := range reads {
		for _, o := range r.outputs {
			examples[o.kind] = append(examples[o.kind], o.Example)
		}
	}
	out := make([]Format, 0, len(Kinds))
	for _, k := range Kinds {
		f := Format{Kind: k, Guides: guides[k], Examples: newest(examples[k], examplesPerKind)}
		for _, m := range methods {
			if !slices.Contains(f.Skills, m) && writesKind(m, k) {
				f.Skills = append(f.Skills, m)
			}
		}
		out = append(out, f)
	}
	return out
}

func newest(examples []Example, n int) []Example {
	slices.SortFunc(examples, func(a, b Example) int {
		return cmp.Or(b.At.Compare(a.At), strings.Compare(b.Source, a.Source), cmp.Compare(b.Offset, a.Offset))
	})
	return examples[:min(n, len(examples))]
}

// Instruction files of the user, of each place and of each place's memory
// 1. the CLAUDE.md of the config and every rules file
// 2. CLAUDE.md and .claude/CLAUDE.md of each place
// 3. the memory files of each place's project folder other than its MEMORY.md index
func scanGuides(config string, places []Place) map[Kind][]Guide {
	out := map[Kind][]Guide{}
	add := func(file, root string) {
		for k, gs := range guidesIn(file, root) {
			out[k] = append(out[k], gs...)
		}
	}
	add(filepath.Join(config, "CLAUDE.md"), "")
	_ = filepath.WalkDir(filepath.Join(config, "rules"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") {
			add(path, "")
		}
		return nil
	})
	for _, p := range places {
		add(filepath.Join(p.Root, "CLAUDE.md"), p.Root)
		add(filepath.Join(p.Root, ".claude", "CLAUDE.md"), p.Root)
		memories, _ := filepath.Glob(filepath.Join(config, "projects", projectDir(p.Root), "memory", "*.md"))
		for _, m := range memories {
			if filepath.Base(m) != "MEMORY.md" {
				add(m, p.Root)
			}
		}
	}
	return out
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9-]`)

// The folder Claude Code keeps the transcripts and memory of a directory in
func projectDir(root string) string { return unsafePath.ReplaceAllString(root, "-") }

// Sections of one file whose heading names a kind
// 1. A section runs to the next heading of the same or a higher level so child headings belong to it
// 2. A child heading naming a kind its parent already names adds nothing
// 3. The front matter name counts as a heading on line 1
// A description is left out since it names a kind as often as context as it names the rule
func guidesIn(file, root string) map[Kind][]Guide {
	hs, err := headings(file)
	if err != nil {
		return nil
	}
	out := map[Kind][]Guide{}
	if name := frontmatter(file, "name"); name != "" {
		for _, k := range kindsIn(name) {
			out[k] = append(out[k], Guide{File: file, Heading: name, Line: 1, Root: root})
		}
	}
	type open struct {
		level int
		kinds []Kind
	}
	var stack []open
	for _, h := range hs {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		var kinds []Kind
		for _, k := range kindsIn(h.text) {
			if !slices.ContainsFunc(stack, func(o open) bool { return slices.Contains(o.kinds, k) }) {
				kinds = append(kinds, k)
				out[k] = append(out[k], Guide{File: file, Heading: h.text, Line: h.line, Root: root})
			}
		}
		stack = append(stack, open{h.level, kinds})
	}
	return out
}

type markdownHeading struct {
	line  int
	level int
	text  string
}

var heading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)

// The headings of a markdown file outside its front matter and fenced code blocks
func headings(file string) ([]markdownHeading, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []markdownHeading
	fence, front := "", false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for n := 1; sc.Scan(); n++ {
		trimmed := strings.TrimSpace(sc.Text())
		switch {
		case n == 1 && trimmed == "---":
			front = true
		case front:
			front = trimmed != "---"
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		default:
			if m := heading.FindStringSubmatch(sc.Text()); m != nil {
				out = append(out, markdownHeading{n, len(m[1]), m[2]})
			}
		}
	}
	return out, sc.Err()
}

// A skill writes a kind when its name or description names that output
func writesKind(m Method, k Kind) bool {
	text := strings.ToLower(strings.ReplaceAll(m.Name, "-", " ") + " " + m.Description)
	return slices.ContainsFunc(describedOutputs[k], func(w string) bool { return names(text, w) })
}

// The kinds whose keywords the text names in the order of Kinds
func kindsIn(text string) []Kind {
	lower := strings.ToLower(text)
	var out []Kind
	for _, k := range Kinds {
		if slices.ContainsFunc(keywords[k], func(w string) bool { return names(lower, w) }) {
			out = append(out, k)
		}
	}
	return out
}

// Whether lower holds the keyword at the start of a word or anywhere for a Korean keyword
func names(lower, keyword string) bool {
	for i := 0; ; {
		at := strings.Index(lower[i:], keyword)
		if at < 0 {
			return false
		}
		at += i
		before, _ := utf8.DecodeLastRuneInString(lower[:at])
		if at == 0 || !isWordRune(before) || !isASCII(keyword) {
			return true
		}
		i = at + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
