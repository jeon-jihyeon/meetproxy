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
				r, err := store.Open("s1", "o", time.Now())
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

func TestRunHookGuard(t *testing.T) {
	t.Parallel()
	bash := func(cmd string) map[string]any {
		return map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
	}
	slack := func(tool, channel string) map[string]any {
		return map[string]any{
			"session_id": "s1", "tool_name": "mcp__plugin_slack_slack__" + tool,
			"tool_input": map[string]any{"channel_id": channel, "message": "m"},
		}
	}

	type args struct {
		open          bool
		corruptConfig bool
		input         map[string]any
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"allows a send to an allowed channel", args{true, false, slack("slack_send_message", "C1")}, ""},
		{"denies a send to another channel", args{true, false, slack("slack_send_message", "C9")}, "slack:C9"},
		{"checks scheduled sends", args{true, false, slack("slack_schedule_message", "C9")}, "slack:C9"},
		{"denies a send without a channel", args{true, false, slack("slack_send_message", "")}, "unknown destination"},
		{"treats drafts as not posting", args{true, false, slack("slack_send_message_draft", "C9")}, ""},
		{"treats Slack reads as not posting", args{true, false, slack("slack_read_thread", "C9")}, ""},
		{"denies a reply to another repository", args{true, false, bash("gh api repos/x/r/pulls/1/comments/2/replies -f body=hi")}, "github:x/r"},
		{"allows a reply to an allowed repository", args{true, false, bash("gh api repos/o/r/pulls/1/comments/2/replies -f body=hi")}, ""},
		{"treats -XPOST as posting", args{true, false, bash("gh api -XPOST repos/x/r/issues/1/comments")}, "github:x/r"},
		{"allows pr comment with --repo", args{true, false, bash("gh pr comment 12 --repo o/r --body hi")}, ""},
		{"denies pr comment with another -R", args{true, false, bash("gh pr comment 12 -R x/r --body hi")}, "github:x/r"},
		{"denies pr comment without a repository", args{true, false, bash("gh pr comment 12 --body hi")}, "unknown destination"},
		{
			"checks every chained command",
			args{true, false, bash("gh api repos/o/r/pulls/1/comments/2/replies -f body=a && gh pr comment 5 --repo x/r --body b")},
			"github:x/r",
		},
		{"denies direct API calls", args{true, false, bash("curl -X POST https://slack.com/api/chat.postMessage -d text=hi")}, "unknown destination"},
		{"treats gh reads as not posting", args{true, false, bash("gh api repos/x/r/pulls/1/comments")}, ""},
		{"allows Bash without gh", args{true, false, bash("ls -al")}, ""},
		{"fails closed on a corrupt config", args{true, true, slack("slack_send_message", "C1")}, "posting check failed"},
		{"skips sessions without an open relay", args{false, false, slack("slack_send_message", "C9")}, ""},
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
			if tc.args.corruptConfig {
				require.NoError(t, os.WriteFile(filepath.Join(data, "dest.json"), []byte("{"), 0o644))
			}
			if tc.args.open {
				_, err := relay.New(data).Open("s1", "o", time.Now())
				require.NoError(t, err)
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
		{"guard denies without a data dir", args{false, "guard", "{}"}, want{false, "posting check failed"}},
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
