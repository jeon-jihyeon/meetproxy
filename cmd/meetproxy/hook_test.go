package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

func hookJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// Returns the deny reason or an empty string when nothing was printed
func denyReason(t *testing.T, out []byte) string {
	t.Helper()
	if len(out) == 0 {
		return ""
	}
	var d struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(out, &d))
	require.Equal(t, "deny", d.HookSpecificOutput.PermissionDecision)
	return d.HookSpecificOutput.PermissionDecisionReason
}

func TestRunHookPath(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "svc", "pkg/alloc.go")
	file := filepath.Join(repo, "pkg", "alloc.go")

	type args struct {
		open  bool
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{
			"records an absolute Read path",
			args{true, map[string]any{"session_id": "s1", "tool_name": "Read", "tool_input": map[string]any{"file_path": file}}},
			[]string{filepath.Join("pkg", "alloc.go")},
		},
		{
			"resolves a relative Grep path against cwd",
			args{true, map[string]any{
				"session_id": "s1", "tool_name": "Grep", "cwd": repo,
				"tool_input": map[string]any{"pattern": "x", "path": "pkg"},
			}},
			[]string{"pkg"},
		},
		{
			"ignores Grep without a path",
			args{true, map[string]any{"session_id": "s1", "tool_name": "Grep", "tool_input": map[string]any{"pattern": "x"}}},
			[]string{},
		},
		{
			"ignores paths outside a repository",
			args{true, map[string]any{"session_id": "s1", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/nowhere/x.go"}}},
			[]string{},
		},
		{
			"ignores input without a session id",
			args{true, map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": file}}},
			[]string{},
		},
		{
			"ignores sessions without an open relay",
			args{false, map[string]any{"session_id": "s1", "tool_name": "Read", "tool_input": map[string]any{"file_path": file}}},
			[]string{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			store := relay.New(data)
			var id string
			if tc.args.open {
				r, err := store.Open("s1", "o", "", time.Now())
				require.NoError(t, err)
				id = r.Id
			}
			err := runHook(data, []string{"path"}, time.Now(), strings.NewReader(hookJSON(t, tc.args.input)), &bytes.Buffer{})
			require.NoError(t, err)
			ps, err := store.Observed(id)
			require.NoError(t, err)
			got := []string{}
			for _, p := range ps {
				got = append(got, p.Rel)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// Which locations a call names and which the relay lets through is covered in the guard package
// These cases cover the hook plumbing around it
func TestRunHookGuard(t *testing.T) {
	t.Parallel()
	bash := func(session, cmd string) map[string]any {
		return map[string]any{"session_id": session, "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	}
	slack := func(tool, channel string) map[string]any {
		return map[string]any{
			"session_id": "s1", "tool_name": "mcp__plugin_slack_slack__" + tool,
			"tool_input": map[string]any{"channel_id": channel, "message": "m"},
		}
	}

	type args struct {
		open    bool
		corrupt string
		input   map[string]any
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"allows a send to an allowed channel", args{true, "", slack("slack_send_message", "C1")}, ""},
		{"denies a send to another channel", args{true, "", slack("slack_send_message", "C9")}, "slack:C9 is not allowed"},
		{"allows a reply where the request came from", args{true, "", slack("slack_send_message", "C7")}, ""},
		{"allows a review on the target", args{true, "", bash("s1", "gh pr review https://github.com/t/r/pull/3 --comment -b hi")}, ""},
		{"denies another pull request of the target repository", args{true, "", bash("s1", "gh pr comment 4 -R t/r -b hi")}, "github:t/r#4"},
		{"allows an allowed repository", args{true, "", bash("s1", "gh pr comment 12 --repo o/r --body hi")}, ""},
		{"denies a post whose destination is unknown", args{true, "", bash("s1", "gh pr comment 12 --body hi")}, "unknown destination"},
		{"allows Bash without gh", args{true, "", bash("s1", "ls -al")}, ""},
		{"fails closed on a corrupt config", args{true, "dest.json", slack("slack_send_message", "C9")}, "posting check failed"},
		{"skips sessions without an open relay", args{false, "", slack("slack_send_message", "C9")}, ""},
		{"lets other commands run with a corrupt relay", args{true, "relay", bash("s1", "ls -al")}, ""},
		{"fails closed on posts with a corrupt relay", args{true, "relay", bash("s1", "gh pr comment 1 -R o/r -b hi")}, "remove it to reset"},
		{"fails closed on a post without a session", args{true, "", bash("", "gh pr comment 1 -R x/r -b hi")}, "names no session"},
		{"lets other commands run without a session", args{true, "", bash("", "ls -al")}, ""},
		{"fails closed on tool input that is no object", args{true, "", map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": "gh"}}, "posting check failed"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			for _, p := range []string{"slack:C1", "github:o/*"} {
				code, err := run("allow", []string{p, "--data", data}, "s1", time.Now(), strings.NewReader(""), &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			if tc.args.open {
				_, err := relay.New(data).Open("s1", "https://w.slack.com/archives/C7/p1", "https://github.com/t/r/pull/3", time.Now())
				require.NoError(t, err)
			}
			broken := map[string]string{"dest.json": "dest.json", "relay": filepath.Join("relay", "open", "s1.json")}
			if f, ok := broken[tc.args.corrupt]; ok {
				require.NoError(t, os.WriteFile(filepath.Join(data, f), []byte("{"), 0o644))
			}
			var out bytes.Buffer
			err := runHook(data, []string{"guard"}, time.Now(), strings.NewReader(hookJSON(t, tc.args.input)), &out)
			require.NoError(t, err)
			reason := denyReason(t, out.Bytes())
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
		})
	}
}

// A take before the relay opens and a close before the turn ends keep the scope of the request
func TestRunHookGuard_Scope(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C7/p1"
	target := "https://github.com/t/r/pull/3"
	id := inbox.IdOf(link)
	bash := func(cmd string) map[string]any {
		return map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	}
	elsewhere := map[string]any{
		"session_id": "s1", "tool_name": "mcp__plugin_slack_slack__slack_send_message",
		"tool_input": map[string]any{"channel_id": "C9", "message": "m"},
	}
	queued := step{"inbox", []string{"add", link, "--verdict", "handle", "--trusted", "yes", "--target", target}, ""}
	taken := []step{queued, {"inbox", []string{"take", id}, ""}}
	closed := append(slices.Clone(taken), step{"open", []string{link}, ""}, step{"close", []string{"--topic", "t"}, ""})

	type args struct {
		setup []step
		stop  bool
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"lets a post go anywhere when nothing is handled", args{nil, false, elsewhere}, ""},
		{"denies a post elsewhere after a take before the relay opens", args{taken, false, elsewhere}, "slack:C9 is not allowed"},
		{"allows a post to the target after a take", args{taken, false, bash("gh pr comment 3 -R t/r -b hi")}, ""},
		{"denies an approval the delegation does not allow", args{taken, false, bash("gh pr review 3 -R t/r --approve -b ok")}, "is not delegated"},
		{"denies meetproxy allow while a take is open", args{taken, false, bash("meetproxy allow slack:C9")}, "changes meetproxy settings"},
		{"denies opening another request while a take is open", args{taken, false, bash("meetproxy open https://w.slack.com/archives/C9/p2")}, "slack:C9 is not allowed"},
		{"denies a post elsewhere after a close in the same turn", args{closed, false, elsewhere}, "slack:C9 is not allowed"},
		{"lets a post go anywhere once the turn of the close ended", args{closed, true, elsewhere}, ""},
		{
			"denies a post elsewhere after a settle in the same turn",
			args{append(slices.Clone(taken), step{"inbox", []string{"done", id}, ""}), false, elsewhere}, "slack:C9 is not allowed",
		},
		{"denies gh in a process substitution read as stdin", args{taken, false, bash("cat < <(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies gh in a substitution as the output file", args{taken, false, bash(`echo x > "$(gh pr comment 1 -R x/r -b hi)"`)}, "github:x/r#1"},
		{"denies gh in a substitution as the error file", args{taken, false, bash("ls 2>$(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies gh in a process substitution behind exec", args{taken, false, bash("exec 3> >(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies a repository on another host", args{taken, false, bash("gh pr comment 3 -R ghe.acme.io/t/r -b hi")}, "a repository on host ghe.acme.io"},
		{"denies GH_REPO on another host", args{taken, false, bash("GH_REPO=ghe.acme.io/t/r gh pr comment 3 -b hi")}, "a repository on host ghe.acme.io"},
		{"denies GH_HOST of another host", args{taken, false, bash("GH_HOST=ghe.acme.io gh pr comment 3 -R t/r -b hi")}, "gh on host ghe.acme.io"},
		{"allows the target on github.com by host", args{taken, false, bash("gh pr comment 3 -R github.com/t/r -b hi")}, ""},
		{"denies meetproxy hook stop after a close in the same turn", args{closed, false, bash(`echo '{"session_id":"s1"}' | meetproxy hook stop`)}, "meetproxy hook stop changes meetproxy settings"},
		{"denies removing the scope marker after a close in the same turn", args{closed, false, bash("rm DATA/scope/s1")}, "a file edit in the meetproxy data directory"},
		{"denies moving the ended relay after a close in the same turn", args{closed, false, bash("mv DATA/relay/ended/s1.json /tmp/x")}, "a file edit in the meetproxy data directory"},
		{"denies truncating the request while a take is open", args{taken, false, bash("truncate -s 0 DATA/inbox/" + id + ".json")}, "a file edit in the meetproxy data directory"},
		{"lets meetproxy hook stop run when nothing is handled", args{nil, false, bash(`echo '{"session_id":"s1"}' | meetproxy hook stop`)}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			for _, s := range tc.args.setup {
				code, err := run(s.cmd, append([]string{"--data", data}, s.args...), "s1", time.Now(), strings.NewReader(s.stdin), &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			if tc.args.stop {
				require.NoError(t, runHook(data, []string{"stop"}, time.Now(), strings.NewReader(`{"session_id":"s1"}`), &bytes.Buffer{}))
			}
			var out bytes.Buffer
			input := strings.ReplaceAll(hookJSON(t, tc.args.input), "DATA", data)
			require.NoError(t, runHook(data, []string{"guard"}, time.Now(), strings.NewReader(input), &out))
			reason := denyReason(t, out.Bytes())
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
			assert.NotContains(t, reason, "meetproxy close", "the denial names no way around it")
		})
	}
}

// Returns the lines of the SessionStart notice or nil when nothing was printed
func startNotice(t *testing.T, out []byte) []string {
	t.Helper()
	if len(out) == 0 {
		return nil
	}
	var o struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(out, &o))
	return strings.Split(o.HookSpecificOutput.AdditionalContext, "\n")
}

func TestRunHookStart(t *testing.T) {
	t.Parallel()
	svc := gitRepo(t, "svc", "main.go")
	other := t.TempDir()
	now := time.Now()
	type spec struct {
		link     string
		name     string
		place    string
		status   inbox.Status
		addedAgo time.Duration
	}
	specs := []spec{
		{"https://w.slack.com/archives/C1/p1", "svc", "", inbox.StatusNew, 3 * time.Minute},
		{"https://w.slack.com/archives/C1/p2", "svc", svc, inbox.StatusAsk, time.Minute},
		{"https://w.slack.com/archives/C1/p3", "svc", "", inbox.StatusTaken, time.Minute},
		{"https://w.slack.com/archives/C1/p4", "web", "", inbox.StatusNew, time.Minute},
		{"https://w.slack.com/archives/C1/p5", "svc", "", inbox.StatusTaken, 2 * time.Hour},
		{"https://w.slack.com/archives/C1/p6", "svc", "/elsewhere/svc", inbox.StatusNew, time.Minute},
		{"https://w.slack.com/archives/C1/p7", "core", svc, inbox.StatusNew, time.Minute},
	}
	line := func(sp spec) string { return inbox.IdOf(sp.link) + " " + sp.link }
	tcs := []struct {
		name string
		cwd  string
		want []string
	}{
		{
			"names the requests of the place oldest first by root or by name when stored without one", svc,
			[]string{
				"meetproxy: 4 requests wait for svc. Tell the user, and run /meetproxy:handle <id> for the ones they ask for.",
				line(specs[4]), line(specs[0]), line(specs[1]), line(specs[6]),
			},
		},
		{"stays quiet in a place no request waits for", other, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			store := inbox.New(data)
			for i, sp := range specs {
				// Apart by a nanosecond so the order of requests added at once is fixed
				at := now.Add(-sp.addedAgo).Add(time.Duration(i))
				_, _, err := store.Add(inbox.Item{Link: sp.link, Name: sp.name, Place: sp.place, Status: sp.status}, at)
				require.NoError(t, err)
			}
			var out bytes.Buffer
			require.NoError(t, runHook(data, []string{"start"}, now, strings.NewReader(hookJSON(t, map[string]any{"cwd": tc.cwd})), &out))
			assert.Equal(t, tc.want, startNotice(t, out.Bytes()))
		})
	}
}

func TestRunHook_BadInput(t *testing.T) {
	t.Parallel()
	type args struct {
		data  bool
		hook  string
		input string
	}
	type want struct {
		err    bool
		reason string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"rejects an unknown hook", args{true, "nope", "{}"}, want{true, ""}},
		{"path hook errors without a data dir", args{false, "path", "{}"}, want{true, ""}},
		{"start hook errors without a data dir", args{false, "start", "{}"}, want{true, ""}},
		{"start hook errors on broken input", args{true, "start", "{"}, want{true, ""}},
		{"guard passes non posting calls without a data dir", args{false, "guard", "{}"}, want{false, ""}},
		{
			"guard denies posts without a data dir",
			args{false, "guard", `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"gh pr comment 1 -R o/r -b hi"}}`},
			want{false, "posting check failed"},
		},
		{"guard denies broken input", args{true, "guard", "{"}, want{false, "posting check failed"}},
		{"stop hook errors without a data dir", args{false, "stop", "{}"}, want{true, ""}},
		{"stop hook passes input without a session", args{true, "stop", "{}"}, want{false, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := ""
			if tc.args.data {
				data = t.TempDir()
			}
			var out bytes.Buffer
			err := runHook(data, []string{tc.args.hook}, time.Now(), strings.NewReader(tc.args.input), &out)
			reason := denyReason(t, out.Bytes())
			assert.Equal(t, tc.want.err, err != nil)
			assert.Contains(t, reason, tc.want.reason)
			assert.Equal(t, tc.want.reason == "", reason == "")
		})
	}
}

// A file tool may not edit the data directory while a request is handled
func TestRunHookGuard_FileEdits(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		open bool
		tool string
		file func(data string) string
		want string
	}{
		{"a write of the allow list while a request is handled", true, "Write", func(d string) string { return filepath.Join(d, "dest.json") }, "meetproxy data directory"},
		{"an edit of a relay while a request is handled", true, "Edit", func(d string) string { return filepath.Join(d, "relay", "open", "s1.json") }, "meetproxy data directory"},
		{"an edit of code while a request is handled", true, "Edit", func(string) string { return "/tmp/svc/main.go" }, ""},
		{"a write of the data directory with no request", false, "Write", func(d string) string { return filepath.Join(d, "dest.json") }, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			if tc.open {
				_, err := relay.New(data).Open("s1", "https://w.slack.com/archives/C7/p1", "", time.Now())
				require.NoError(t, err)
			}
			input := map[string]any{"session_id": "s1", "tool_name": tc.tool, "tool_input": map[string]any{"file_path": tc.file(data)}}
			var out bytes.Buffer

			require.NoError(t, runHook(data, []string{"guard"}, time.Now(), strings.NewReader(hookJSON(t, input)), &out))

			reason := denyReason(t, out.Bytes())
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
		})
	}
}
