package workmap_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/workmap"
)

// An assistant line with one tool use of id sent at minute of 2030-01-01
func sends(t *testing.T, cwd, id string, minute int, name string, input map[string]any) string {
	at := time.Date(2030, 1, 1, 0, minute, 0, 0, time.UTC).Format(time.RFC3339)
	use := map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
	return line(t, map[string]any{"type": "assistant", "cwd": cwd, "timestamp": at, "message": map[string]any{"content": []any{use}}})
}

// A user line with the result of the tool use of id
func result(t *testing.T, id string, failed bool) string {
	res := map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": failed, "content": "done"}
	return line(t, map[string]any{"type": "user", "message": map[string]any{"content": []any{res}}})
}

func bash(command string) map[string]any { return map[string]any{"command": command} }

// Every example as its kind and text in the order of the formats
func examples(t *testing.T, s workmap.Store) []string {
	t.Helper()
	formats, err := s.Formats()
	require.NoError(t, err)
	var out []string
	for _, f := range formats {
		for _, e := range f.Examples {
			out = append(out, string(f.Kind)+" "+e.Text)
		}
	}
	return out
}

func TestFormatsGuides(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	writeFile(t, filepath.Join(m.config, "CLAUDE.md"), "# Personal Settings\n\n"+
		"# Commit Messages\n\n- Korean\n\n## Commit body\n\n- none\n\n"+
		"# PR Descriptions\n\n```markdown\n# Commit inside a fence\n## 동기\n```\n\n"+
		"# Linear\n\n## Issue Body\n\n## 슬랙 공지\n\n"+
		"# Prompt rules\n\n# 리뷰 답변\n")
	writeFile(t, filepath.Join(m.config, "rules", "git.md"), "---\npaths: \"**/*.go\"\n---\n# Git\n\n## Branch Naming ##\n")
	writeFile(t, filepath.Join(m.svc, "CLAUDE.md"), "# svc\n\n## 커밋 규칙\n")
	writeFile(t, filepath.Join(m.svc, ".claude", "CLAUDE.md"), "# Email replies\n")
	memory := filepath.Join(m.config, "projects", regexp.MustCompile(`[^A-Za-z0-9-]`).ReplaceAllString(m.svc, "-"), "memory")
	writeFile(t, filepath.Join(memory, "tone.md"), "---\nname: 슬랙 답장 톤\ndescription: 짧게\n---\n\nbody\n")
	writeFile(t, filepath.Join(memory, "oncall.md"), "---\nname: 온콜 순서\ndescription: 슬랙 문서 커밋 이메일 정리\n---\n\nbody\n")
	writeFile(t, filepath.Join(memory, "MEMORY.md"), "# Slack index\n")
	transcript(t, m.config, "a", false, line(t, prompt(m.svc, "할당 우선순위 로직", nil)))
	s := workmap.New(t.TempDir())
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)

	claude, rules := filepath.Join(m.config, "CLAUDE.md"), filepath.Join(m.config, "rules", "git.md")
	tcs := []struct {
		kind string
		want []string
	}{
		{"commit", []string{claude + ":3 Commit Messages ", filepath.Join(m.svc, "CLAUDE.md") + ":3 커밋 규칙 " + m.svc}},
		{"pull-request", []string{claude + ":11 PR Descriptions "}},
		{"review-comment", []string{claude + ":26 리뷰 답변 "}},
		{"linear-issue", []string{claude + ":18 Linear "}},
		{"slack-message", []string{claude + ":22 슬랙 공지 ", filepath.Join(memory, "tone.md") + ":1 슬랙 답장 톤 " + m.svc}},
		{"email", []string{filepath.Join(m.svc, ".claude", "CLAUDE.md") + ":1 Email replies " + m.svc}},
		{"branch", []string{rules + ":6 Branch Naming "}},
		{"sheet", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			f, err := s.Format(tc.kind)
			require.NoError(t, err)
			var got []string
			for _, g := range f.Guides {
				got = append(got, fmt.Sprintf("%s:%d %s %s", g.File, g.Line, g.Heading, g.Root))
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestFormatsExamples(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	dir := t.TempDir()
	body := filepath.Join(dir, "body.md")
	writeFile(t, body, "## 동기\n\nfrom a file")
	// A path reused after the command ran holds a later text
	later := filepath.Join(dir, "later.md")
	writeFile(t, later, "written later")
	require.NoError(t, os.Chtimes(later, time.Now(), time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)))
	// Opening a pipe for reading blocks until a writer comes
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))
	ok := func(id string, minute int, name string, input map[string]any) []string {
		return []string{sends(t, m.svc, id, minute, name, input), result(t, id, false)}
	}
	gitCommit := func(id string, minute int, msg string) []string {
		return ok(id, minute, "Bash", bash("git add . && git commit -q -m '"+msg+"' && git push"))
	}

	tcs := []struct {
		name  string
		lines [][]string
		want  []string
	}{
		{
			"git commits from -m, a cat here document and a stdin here document",
			[][]string{
				gitCommit("a", 1, "feat(x): one"),
				ok("b", 2, "Bash", bash("git -C /r commit -m \"$(cat <<'EOF'\nfix(x): it's two\n\nbody \"quoted\"\nEOF\n)\"")),
				ok("c", 3, "Bash", bash("git commit -F - <<EOF\nchore(x): three\nEOF\necho done")),
			},
			[]string{"commit chore(x): three", "commit fix(x): it's two\n\nbody \"quoted\"", "commit feat(x): one"},
		},
		{
			"only the newest three examples of a kind stay",
			[][]string{gitCommit("a", 1, "c1"), gitCommit("b", 4, "c4"), gitCommit("c", 2, "c2"), gitCommit("d", 3, "c3")},
			[]string{"commit c4", "commit c3", "commit c2"},
		},
		{
			"a tool use whose result failed or never came is skipped",
			[][]string{
				{sends(t, m.svc, "a", 1, "Bash", bash("git commit -m failed")), result(t, "a", true)},
				{sends(t, m.svc, "b", 2, "Bash", bash("git commit -m pending"))},
			},
			nil,
		},
		{
			"a git command inside a here document of another command is not run",
			[][]string{ok("a", 1, "Bash", bash("python3 - <<'EOF'\nimport os\nos.system(\"git commit -m inner\")\ngit commit -m inner\nEOF"))},
			nil,
		},
		{
			"gh pull requests, reviews, comments and issues",
			[][]string{
				ok("a", 1, "Bash", bash("gh pr create --title 'feat: x' --body-file "+body+" --base main")),
				ok("b", 2, "Bash", bash("gh pr comment 1 --body 'LGTM'")),
				ok("c", 3, "Bash", bash("gh pr review 1 --comment -b \"Looks fine\"")),
				ok("d", 4, "Bash", bash("gh api repos/o/r/pulls/1/comments/2/replies -X POST -f body='Fixed in abc'")),
				ok("e", 5, "Bash", bash("gh issue create -t 'Bug' -b 'Steps'")),
				ok("f", 6, "Bash", bash("gh issue comment 3 --body=Thanks")),
				ok("g", 7, "Bash", bash("gh api repos/o/r/pulls/1/comments")),
			},
			[]string{
				"pull-request feat: x\n\n## 동기\n\nfrom a file",
				"review-comment Fixed in abc", "review-comment Looks fine", "review-comment LGTM",
				"issue Thanks", "issue Bug\n\nSteps",
			},
		},
		{
			"mcp tools that send",
			[][]string{
				ok("a", 1, "mcp__plugin_slack_slack__slack_send_message", map[string]any{"channel_id": "C1", "message": "배포 완료"}),
				ok("b", 2, "mcp__linear__save_issue", map[string]any{"title": "할당 정리", "description": "## 동기"}),
				ok("c", 3, "mcp__linear__save_issue", map[string]any{"id": "SER-1", "state": "Done"}),
				ok("d", 4, "mcp__claude_ai_Gmail__create_draft", map[string]any{"subject": "Hi", "body": "Mail body"}),
				ok("e", 5, "mcp__google_sheets__append_values", map[string]any{"values": []any{[]any{"a", 1}}}),
				ok("f", 6, "mcp__atlassian__createConfluencePage", map[string]any{"title": "Runbook", "body": "<p>x</p>"}),
				ok("g", 7, "mcp__claude_ai_Google_Drive__create_file", map[string]any{"title": "Plan", "textContent": "text"}),
				ok("k", 11, "mcp__claude_ai_Google_Drive__create_file", map[string]any{"title": "Rows", "textContent": "a,1", "contentMimeType": "text/csv"}),
				ok("h", 8, "mcp__meetproxy__post", map[string]any{"link": "https://acme.slack.com/archives/C1/p1", "text": "답변"}),
				ok("i", 9, "mcp__meetproxy__post", map[string]any{"link": "https://github.com/o/r/pull/1#discussion_r2", "text": "Reply"}),
				ok("j", 10, "mcp__plugin_slack_slack__slack_read_thread", map[string]any{"channel_id": "C1"}),
			},
			[]string{
				"review-comment Reply", "linear-issue 할당 정리\n\n## 동기", "slack-message 답변", "slack-message 배포 완료",
				"email Hi\n\nMail body", "sheet Rows\n\na,1", "sheet [[\"a\",1]]", "document Plan\n\ntext", "document Runbook\n\n<p>x</p>",
			},
		},
		{
			"branches from worktree add, checkout and switch",
			[][]string{
				ok("a", 1, "Bash", bash("git worktree add ../svc-1 -b jed/feat/x 2>&1 | tail -1")),
				ok("b", 2, "Bash", bash("git checkout -b jed/fix/y")),
				ok("c", 3, "Bash", bash("git -C /r switch -c jed/chore/z")),
			},
			[]string{"branch jed/chore/z", "branch jed/fix/y", "branch jed/feat/x"},
		},
		{
			"git and gh behind wrappers, global flags and attached values",
			[][]string{
				ok("a", 1, "Bash", bash("cat <<'EOF' | python3 /h/commit-oneline.py --run commit -F -\nfeat: piped\nEOF")),
				ok("b", 2, "Bash", bash("GIT_EDITOR=true git commit -mattached")),
				ok("c", 3, "Bash", bash("git commit -F - <<< 'here string'")),
				ok("d", 4, "Bash", bash("env A=1 gh -R o/r pr create -t Title -b Body")),
			},
			[]string{"commit here string", "commit attached", "commit feat: piped", "pull-request Title\n\nBody"},
		},
		{
			"gh api bodies from a field file and from JSON on stdin",
			[][]string{
				ok("a", 1, "Bash", bash("gh api repos/o/r/pulls/1/comments -F body=@"+body)),
				ok("b", 2, "Bash", bash("gh api repos/o/r/pulls/1/reviews --input - <<'EOF'\n{\"body\": \"From input\"}\nEOF")),
			},
			[]string{"review-comment From input", "review-comment ## 동기\n\nfrom a file"},
		},
		{
			"message files written after the command, pipes and devices are not read",
			[][]string{
				ok("a", 1, "Bash", bash("gh pr create -t '' --body-file "+later)),
				ok("b", 2, "Bash", bash("git commit -F "+fifo)),
				ok("c", 3, "Bash", bash("git commit -F /dev/stdin")),
				ok("d", 4, "Bash", bash("git commit --file=/dev/zero")),
			},
			nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config := newConfig(t)
			var lines []string
			for _, l := range tc.lines {
				lines = append(lines, l...)
			}
			transcript(t, config, "a", false, lines...)
			s := workmap.New(t.TempDir())
			_, err := s.Refresh(config, false, time.Now())
			require.NoError(t, err)
			assert.Equal(t, tc.want, examples(t, s))
		})
	}
}

// Steps share one store since each refresh reads on from the last one
func TestFormatsIncremental(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	data := t.TempDir()
	s := workmap.New(data)
	commit := func(id string, minute int) []string {
		return []string{sends(t, m.svc, id, minute, "Bash", bash("git commit -m "+id)), result(t, id, false)}
	}
	now, dayLater := time.Now(), time.Now().Add(48*time.Hour)
	steps := []struct {
		name  string
		lines []string
		// Drops the state as a refresh that stopped before writing it
		retry bool
		now   time.Time
		want  []string
	}{
		{"the first refresh reads an example", commit("c1", 1), false, now, []string{"commit c1"}},
		{"a refresh without new lines keeps it once", nil, false, now, []string{"commit c1"}},
		{"new lines add to it", commit("c2", 2), false, now, []string{"commit c2", "commit c1"}},
		{"a retry reading the same lines again keeps each once", nil, true, now, []string{"commit c2", "commit c1"}},
		{
			"a tool use waiting for its result adds nothing yet", commit("c3", 3)[:1], false, now,
			[]string{"commit c2", "commit c1"},
		},
		{
			"its result read by a later refresh adds it", commit("c3", 3)[1:], false, now,
			[]string{"commit c3", "commit c2", "commit c1"},
		},
		{
			"a tool use whose transcript stopped changing a day before stops waiting", commit("c4", 4)[:1], false, dayLater,
			[]string{"commit c3", "commit c2", "commit c1"},
		},
		{
			"so its late result adds nothing", commit("c4", 4)[1:], false, dayLater,
			[]string{"commit c3", "commit c2", "commit c1"},
		},
	}
	for _, st := range steps {
		transcript(t, m.config, "a", false, st.lines...)
		if st.retry {
			require.NoError(t, os.Remove(filepath.Join(data, "workmap", "state.json")), st.name)
		}
		_, err := s.Refresh(m.config, false, st.now)
		require.NoError(t, err, st.name)
		assert.Equal(t, st.want, examples(t, s), st.name)
	}
}

func TestFormat(t *testing.T) {
	t.Parallel()
	m := newMachine(t)
	writeFile(t, filepath.Join(m.config, "CLAUDE.md"), "# Commit Messages\n")
	writeFile(t, filepath.Join(m.svc, "CLAUDE.md"), "# Commits\n")
	writeFile(t, filepath.Join(m.svc, ".claude", "skills", "git-wrap", "SKILL.md"), "---\ndescription: Propose a commit message\n---\n")
	writeFile(t, filepath.Join(m.config, "skills", "git-wrap", "SKILL.md"), "---\ndescription: Write a commit message\n---\n")
	transcript(t, m.config, "a", false, line(t, prompt(m.svc, "할당 우선순위 로직", nil)))
	s := workmap.New(t.TempDir())
	_, err := s.Refresh(m.config, false, time.Now())
	require.NoError(t, err)

	type want struct {
		guides  []string
		skills  []string
		unknown bool
	}
	tcs := []struct {
		name string
		kind string
		root string
		want want
	}{
		{"every guide and skill without a place", "commit", "", want{[]string{"Commit Messages", "Commits"}, []string{"git-wrap", "git-wrap"}, false}},
		{"those of the place", "commit", m.svc, want{[]string{"Commit Messages", "Commits"}, []string{"git-wrap", "git-wrap"}, false}},
		{"only those of the user elsewhere", "commit", m.wiki, want{[]string{"Commit Messages"}, []string{"git-wrap"}, false}},
		{"a kind the map knows nothing of", "sheet", "", want{nil, nil, false}},
		{"an unknown kind", "tweet", "", want{nil, nil, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := s.Format(tc.kind)
			f = f.In(tc.root)
			var guides, skills []string
			for _, g := range f.Guides {
				guides = append(guides, g.Heading)
			}
			for _, k := range f.Skills {
				skills = append(skills, k.Name)
			}
			assert.Equal(t, tc.want, want{guides, skills, errors.Is(err, workmap.ErrUnknownKind)})
		})
	}
}
