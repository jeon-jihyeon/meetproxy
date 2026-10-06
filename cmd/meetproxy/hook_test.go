package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			err := runHook(data, []string{"path"}, strings.NewReader(hookJSON(t, tc.args.input)), &bytes.Buffer{})
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
				code, err := run("allow", []string{p, "--data", data}, "s1", time.Now(), &bytes.Buffer{})
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
			err := runHook(data, []string{"guard"}, strings.NewReader(hookJSON(t, tc.args.input)), &out)
			require.NoError(t, err)
			reason := denyReason(t, out.Bytes())
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
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
		{"guard passes non posting calls without a data dir", args{false, "guard", "{}"}, want{false, ""}},
		{
			"guard denies posts without a data dir",
			args{false, "guard", `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"gh pr comment 1 -R o/r -b hi"}}`},
			want{false, "posting check failed"},
		},
		{"guard denies broken input", args{true, "guard", "{"}, want{false, "posting check failed"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := ""
			if tc.args.data {
				data = t.TempDir()
			}
			var out bytes.Buffer
			err := runHook(data, []string{tc.args.hook}, strings.NewReader(tc.args.input), &out)
			reason := denyReason(t, out.Bytes())
			assert.Equal(t, tc.want.err, err != nil)
			assert.Contains(t, reason, tc.want.reason)
			assert.Equal(t, tc.want.reason == "", reason == "")
		})
	}
}
