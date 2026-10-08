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
	powershell := map[string]any{
		"session_id": "s1", "tool_name": "PowerShell", "tool_input": map[string]any{"command": "Get-ChildItem"},
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
		{
			"denies PowerShell while a request is handled",
			args{true, "", powershell}, "a PowerShell call the guard cannot read",
		},
		{"lets PowerShell run when nothing is handled", args{false, "", powershell}, ""},
		{
			"denies a network write while a request is handled",
			args{true, "", bash("s1", "curl -d a=1 https://gitlab.com/api")}, "a curl write to gitlab.com",
		},
		{
			"lets a network write run when nothing is handled",
			args{false, "", bash("s1", "curl -d a=1 https://gitlab.com/api")}, "",
		},
		{
			"allows a network read while a request is handled",
			args{true, "", bash("s1", "curl -s https://gitlab.com/api")}, "",
		},
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
	queued := step{"inbox", []string{"add", link, "--target", target}, ""}
	taken := []step{queued, {"inbox", []string{"take", id}, ""}}
	opened := append(slices.Clone(taken), step{"open", []string{link}, ""})
	closed := append(slices.Clone(opened), step{"close", nil, ""})

	type args struct {
		setup []step
		// Hook that runs after the setup
		hook  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"lets a post go anywhere when nothing is handled", args{nil, "", elsewhere}, ""},
		{"denies a post elsewhere after a take before the relay opens", args{taken, "", elsewhere}, "slack:C9 is not allowed"},
		{"allows a post to the target after a take", args{taken, "", bash("gh pr comment 3 -R t/r -b hi")}, ""},
		{"denies an approval the delegation does not allow", args{taken, "", bash("gh pr review 3 -R t/r --approve -b ok")}, "is not delegated"},
		{"denies meetproxy allow while a take is open", args{taken, "", bash("meetproxy allow slack:C9")}, "changes meetproxy settings"},
		{"denies opening another request while a take is open", args{taken, "", bash("meetproxy open https://w.slack.com/archives/C9/p2")}, "slack:C9 is not allowed"},
		{"denies a post elsewhere after a close in the same turn", args{closed, "", elsewhere}, "slack:C9 is not allowed"},
		{"lets a post go anywhere once the turn of the close ended", args{closed, "stop", elsewhere}, ""},
		{
			"denies a post elsewhere after a settle in the same turn",
			args{append(slices.Clone(taken), step{"inbox", []string{"done", id}, ""}), "", elsewhere}, "slack:C9 is not allowed",
		},
		{"denies gh in a process substitution read as stdin", args{taken, "", bash("cat < <(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies gh in a substitution as the output file", args{taken, "", bash(`echo x > "$(gh pr comment 1 -R x/r -b hi)"`)}, "github:x/r#1"},
		{"denies gh in a substitution as the error file", args{taken, "", bash("ls 2>$(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies gh in a process substitution behind exec", args{taken, "", bash("exec 3> >(gh pr comment 1 -R x/r -b hi)")}, "github:x/r#1"},
		{"denies a repository on another host", args{taken, "", bash("gh pr comment 3 -R ghe.acme.io/t/r -b hi")}, "a repository on host ghe.acme.io"},
		{"denies GH_REPO on another host", args{taken, "", bash("GH_REPO=ghe.acme.io/t/r gh pr comment 3 -b hi")}, "a repository on host ghe.acme.io"},
		{"denies GH_HOST of another host", args{taken, "", bash("GH_HOST=ghe.acme.io gh pr comment 3 -R t/r -b hi")}, "gh on host ghe.acme.io"},
		{"allows the target on github.com by host", args{taken, "", bash("gh pr comment 3 -R github.com/t/r -b hi")}, ""},
		{"denies meetproxy hook stop after a close in the same turn", args{closed, "", bash(`echo '{"session_id":"s1"}' | meetproxy hook stop`)}, "meetproxy hook stop changes meetproxy settings"},
		{"denies removing the scope marker after a close in the same turn", args{closed, "", bash("rm DATA/scope/s1")}, "a file edit in the meetproxy data directory"},
		{"denies moving the ended relay after a close in the same turn", args{closed, "", bash("mv DATA/relay/ended/s1.json /tmp/x")}, "a file edit in the meetproxy data directory"},
		{"denies truncating the request while a take is open", args{taken, "", bash("truncate -s 0 DATA/inbox/" + id + ".json")}, "a file edit in the meetproxy data directory"},
		{"lets a post go anywhere once the session of a take ended", args{taken, "end", elsewhere}, ""},
		{"lets a post go anywhere once the session of an open relay ended", args{opened, "end", elsewhere}, ""},
		{"keeps the scope of a take after a turn ends", args{taken, "stop", elsewhere}, "slack:C9 is not allowed"},
		{"lets meetproxy hook stop run when nothing is handled", args{nil, "", bash(`echo '{"session_id":"s1"}' | meetproxy hook stop`)}, ""},
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
			if tc.args.hook != "" {
				input := strings.NewReader(`{"session_id":"s1"}`)
				require.NoError(t, runHook(data, []string{tc.args.hook}, time.Now(), input, &bytes.Buffer{}))
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
		{"path hook is gone", args{true, "path", "{}"}, want{true, ""}},
		{"start hook is gone", args{true, "start", "{}"}, want{true, ""}},
		{"end hook errors without a data dir", args{false, "end", "{}"}, want{true, ""}},
		{"end hook errors on broken input", args{true, "end", "{"}, want{true, ""}},
		{"end hook passes input without a session", args{true, "end", "{}"}, want{false, ""}},
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

// The end of a session gives its takes back, closes its relay and drops its marker
func TestRunHookEnd(t *testing.T) {
	t.Parallel()
	mine, other := "https://w.slack.com/archives/C1/p1", "https://w.slack.com/archives/C1/p2"
	type args struct {
		setup []step
		input string
	}
	type want struct {
		statuses map[string]inbox.Status
		reason   string
		relay    bool
		markers  []string
	}
	add := func(link string) step { return step{"inbox", []string{"add", link}, ""} }
	take := func(link, session string) step {
		return step{"inbox", []string{"take", inbox.IdOf(link), "--session", session}, ""}
	}
	statuses := func(m, o inbox.Status) map[string]inbox.Status {
		return map[string]inbox.Status{inbox.IdOf(mine): m, inbox.IdOf(other): o}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"opens the take of the session again",
			args{[]step{add(mine), add(other), take(mine, "s1")}, `{"session_id":"s1"}`},
			want{statuses(inbox.StatusOpen, inbox.StatusOpen), inbox.Released, false, []string{}},
		},
		{
			"closes the relay of the take",
			args{[]step{add(mine), add(other), take(mine, "s1"), {"open", []string{mine, "--session", "s1"}, ""}}, `{"session_id":"s1"}`},
			want{statuses(inbox.StatusOpen, inbox.StatusOpen), inbox.Released, false, []string{}},
		},
		{
			"leaves the take of another session",
			args{[]step{add(mine), add(other), take(mine, "s1"), take(other, "s2")}, `{"session_id":"s1"}`},
			want{statuses(inbox.StatusOpen, inbox.StatusTaken), inbox.Released, false, []string{"s2"}},
		},
		{
			"does nothing without a session",
			args{[]step{add(mine), add(other), take(mine, "s1")}, `{}`},
			want{statuses(inbox.StatusTaken, inbox.StatusOpen), "", false, []string{"s1"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			now := time.Now()
			for _, s := range tc.args.setup {
				mustRun(t, data, "", now, s)
			}

			require.NoError(t, runHook(data, []string{"end"}, now, strings.NewReader(tc.args.input), &bytes.Buffer{}))

			store := inbox.New(data)
			got := map[string]inbox.Status{}
			for id := range tc.want.statuses {
				it, err := store.Get(id)
				require.NoError(t, err)
				got[id] = it.Status
			}
			it, err := store.Get(inbox.IdOf(mine))
			require.NoError(t, err)
			_, err = relay.New(data).Current("s1")
			assert.Equal(t, tc.want, want{got, it.Reason, err == nil, markers(t, data)})
		})
	}
}
