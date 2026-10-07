package guard_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/guard"
)

type call struct {
	tool  string
	input string
}

func bash(t *testing.T, cmd string) call {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": cmd})
	require.NoError(t, err)
	return call{"Bash", string(b)}
}

func slack(t *testing.T, tool, channel string) call {
	t.Helper()
	b, err := json.Marshal(map[string]string{"channel_id": channel, "message": "m"})
	require.NoError(t, err)
	return call{"mcp__plugin_slack_slack__" + tool, string(b)}
}

func github(t *testing.T, tool string, input map[string]any) call {
	t.Helper()
	b, err := json.Marshal(input)
	require.NoError(t, err)
	return call{"mcp__github__" + tool, string(b)}
}

// The relay comes from a Slack thread in C7 and works on pull request 3 of t/r
// The allow list holds slack:C1 and github:o/*
func TestCheck(t *testing.T) {
	t.Parallel()
	allow := dest.New(t.TempDir())
	for _, p := range []string{"slack:C1", "github:o/*"} {
		require.NoError(t, allow.Add(p))
	}
	origin, ok := dest.Parse("https://w.slack.com/archives/C7/p1")
	require.True(t, ok)
	target, ok := dest.Parse("https://github.com/t/r/pull/3")
	require.True(t, ok)

	tcs := []struct {
		name string
		call call
		want string
	}{
		{"allows a send to an allowed channel", slack(t, "slack_send_message", "C1"), ""},
		{"denies a send to another channel", slack(t, "slack_send_message", "C9"), "slack:C9"},
		{"allows a reply where the request came from", slack(t, "slack_send_message", "C7"), ""},
		{"checks scheduled sends", slack(t, "slack_schedule_message", "C9"), "slack:C9"},
		{"denies a send without a channel", slack(t, "slack_send_message", ""), "unknown destination"},
		{"denies a canvas without a channel", slack(t, "slack_create_canvas", ""), "unknown destination"},
		{"treats drafts as not posting", slack(t, "slack_send_message_draft", "C9"), ""},
		{"treats Slack reads as not posting", slack(t, "slack_read_thread", "C9"), ""},
		{"checks a reaction", slack(t, "slack_add_reaction", "C9"), "slack:C9"},
		{"allows a review on the target", bash(t, "gh pr review https://github.com/t/r/pull/3 --comment -b hi"), ""},
		{"allows a comment on the target number", bash(t, "gh pr comment 3 -R t/r -b hi"), ""},
		{"denies another number of the target repository", bash(t, "gh pr comment 4 -R t/r -b hi"), "github:t/r#4"},
		{"denies the target repository without a number", bash(t, "gh pr create -R t/r -t x -b y"), "github:t/r"},
		{"denies an api post to another number of the target", bash(t, "gh api repos/t/r/issues/4/comments -f body=hi"), "github:t/r#4"},
		{"allows an api post to the target", bash(t, "gh api repos/t/r/pulls/3/comments/2/replies -f body=hi"), ""},
		{"denies another repository of the target owner", bash(t, "gh pr comment 1 -R t/x -b hi"), "github:t/x#1"},
		{"denies a reply to another repository", bash(t, "gh api repos/x/r/pulls/1/comments/2/replies -f body=hi"), "github:x/r#1"},
		{"allows a reply to an allowed repository", bash(t, "gh api repos/o/r/pulls/1/comments/2/replies -f body=hi"), ""},
		{"treats -XPOST as posting", bash(t, "gh api -XPOST repos/x/r/issues/1/comments"), "github:x/r#1"},
		{"treats a lower case method as posting", bash(t, "gh api --method=post repos/x/r/issues/1/comments"), "github:x/r#1"},
		{"reads the endpoint after a header", bash(t, "gh api -H 'Accept: x' repos/x/r/issues/1/comments --method POST"), "github:x/r#1"},
		{"treats a file field as posting", bash(t, "gh api repos/x/r/issues/1/comments -F body=@f"), "github:x/r#1"},
		{"allows pr comment with --repo", bash(t, "gh pr comment 12 --repo o/r --body hi"), ""},
		{"denies pr comment with another -R", bash(t, "gh pr comment 12 -R x/r --body hi"), "github:x/r#12"},
		{"denies pr comment without a repository", bash(t, "gh pr comment 12 --body hi"), "unknown destination"},
		{
			"checks every chained command",
			bash(t, "gh api repos/o/r/pulls/1/comments/2/replies -f body=a && gh pr comment 5 --repo x/r --body b"),
			"github:x/r#5",
		},
		{"checks every piped command", bash(t, "gh pr comment 1 -R o/r -b hi | gh pr comment 2 -R x/r -b hi"), "github:x/r#2"},
		{"denies direct API calls", bash(t, "curl -X POST https://slack.com/api/chat.postMessage -d text=hi"), "unknown destination"},
		{"treats gh reads as not posting", bash(t, "gh api repos/x/r/pulls/1/comments"), ""},
		{"allows Bash without gh", bash(t, "ls -al"), ""},
		{
			"checks every repository named, not the first one",
			bash(t, `gh pr comment 1 --body "see https://github.com/o/r/blob/main/a.go" --repo x/r`),
			"github:x/r#1",
		},
		{"ignores links in a body when the target is allowed", bash(t, `gh pr comment 1 --repo o/r --body "see https://github.com/x/r"`), ""},
		{"allows a pr link as the target", bash(t, "gh pr comment https://github.com/o/r/pull/1 -b hi"), ""},
		{"denies issue create in another repository", bash(t, "gh issue create -R x/r --title t --body b"), "github:x/r"},
		{"denies pr edit in another repository", bash(t, "gh pr edit 1 --repo=x/r --body b"), "github:x/r#1"},
		{"denies release create without a repository", bash(t, "gh release create v1"), "unknown destination"},
		{"denies gist create", bash(t, "gh gist create a.md"), "unknown destination"},
		{"treats pr view as reading", bash(t, "gh pr view 1 -R x/r --comments"), ""},
		{"treats gh --version as reading", bash(t, "gh --version"), ""},
		{"denies gh without a subcommand", bash(t, "gh -R x/r"), "gh without a subcommand"},
		{"checks GH_REPO", bash(t, "GH_REPO=x/r gh pr comment 1 --repo o/r --body hi"), "github:x/r#1"},
		{"checks GH_REPO given to env", bash(t, "env GH_REPO=x/r gh pr comment 1 --repo o/r --body hi"), "github:x/r#1"},
		{"checks gh behind a wrapper", bash(t, "env FOO=1 timeout 5 gh issue comment 1 -R x/r -b hi"), "github:x/r#1"},
		{"checks gh inside bash -c", bash(t, `bash -c "gh pr comment 1 -R x/r -b hi"`), "github:x/r#1"},
		{"checks gh inside a quoted substitution", bash(t, `echo "$(gh pr comment 1 -R x/r -b hi)"`), "github:x/r#1"},
		{"checks gh inside backticks", bash(t, "echo `gh pr comment 1 -R x/r -b hi`"), "github:x/r#1"},
		{"checks gh inside an assignment", bash(t, `A="$(gh pr comment 1 -R x/r -b hi)"`), "github:x/r#1"},
		{"checks gh inside a nested substitution", bash(t, `echo "$(echo $(gh pr comment 1 -R x/r -b hi))"`), "github:x/r#1"},
		{"checks gh run by a shell reading a here document", bash(t, "bash <<EOF\ngh pr comment 1 -R x/r -b hi\nEOF"), "github:x/r#1"},
		{"denies gh named by a variable", bash(t, "G=gh; $G pr comment 1 -R x/r -b hi"), "unknown destination"},
		{"treats DELETE as writing", bash(t, "gh api --method DELETE repos/x/r/issues/comments/1"), "github:x/r"},
		{"denies graphql writes", bash(t, `gh api graphql -f query="mutation { x }"`), "unknown destination"},
		{"keeps separators inside quotes", bash(t, `gh pr comment 1 -R o/r -b "a; gh is fine"`), ""},
		{"checks a substitution inside a body", bash(t, `gh pr comment 1 -R o/r -b "$(gh pr comment 2 -R x/r -b hi)"`), "github:x/r#2"},
		{"checks prose run by bash -c", bash(t, `bash -c "gh alias set c x"`), "gh alias is not checked"},
		{"allows a GitHub tool post to an allowed repository", github(t, "add_issue_comment", map[string]any{"owner": "o", "repo": "r", "issue_number": 1}), ""},
		{"allows a GitHub tool post to the target", github(t, "create_pull_request_review", map[string]any{"owner": "t", "repo": "r", "pull_number": 3}), ""},
		{"denies a GitHub tool post elsewhere", github(t, "add_issue_comment", map[string]any{"owner": "x", "repo": "r", "issue_number": 1}), "github:x/r#1"},
		{"denies a GitHub tool post without a repository", github(t, "create_repository", map[string]any{"name": "n"}), "unknown destination"},
		{"treats GitHub tool reads as not posting", github(t, "get_issue", map[string]any{"owner": "x", "repo": "r", "issue_number": 1}), ""},
		{"ignores other tools", call{"Read", `{"file_path":"/a"}`}, ""},
		{"denies an unknown tool that saves", call{"mcp__linear__save_comment", `{"issueId":"x","body":"hi"}`}, "unknown destination"},
		{"lets an unknown tool that lists pass", call{"mcp__linear__list_issues", `{}`}, ""},
		{"leaves the post tool to its own check", call{"mcp__meetproxy__post", `{"link":"https://github.com/x/r/pull/1","text":"hi"}`}, ""},
		{"allows meetproxy open of the origin and the target", bash(t, "meetproxy open https://w.slack.com/archives/C7/p1 --target https://github.com/t/r/pull/3"), ""},
		{"allows meetproxy close and inbox done", bash(t, "meetproxy close --topic t && meetproxy inbox done abcdefabcdef"), ""},
		{"allows meetproxy commands that only read", bash(t, "meetproxy locate --data d retry && meetproxy triage && meetproxy allowed"), ""},
		{"allows a meetproxy slack post where the request came from", bash(t, "echo hi | meetproxy slack post https://w.slack.com/archives/C7/p2"), ""},
		{"allows a word that only mentions meetproxy", bash(t, "grep -r meetproxy ."), ""},
		{"allows python without an inline program", bash(t, "python3 -m json.tool a.json"), ""},
		{"allows a shell running a file", bash(t, "bash ./build.sh"), ""},
		{"allows a test and a brace group", bash(t, "[ -f a ] && { ls; }"), ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := check(t, tc.call, origin, target, false, allow)
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
		})
	}
}

// One table per bypass the old guard let through
// a. a link after a flag that takes no value
// b. gh behind a wrapper the guard did not know or a wrapper option with a value
// c. gh alias and unknown subcommands
// d. a target repository given as OWNER/REPO
func TestCheck_Bypass(t *testing.T) {
	t.Parallel()
	allow := dest.New(t.TempDir())
	require.NoError(t, allow.Add("github:o/*"))
	origin, ok := dest.Parse("https://w.slack.com/archives/C7/p1")
	require.True(t, ok)

	tcs := []struct {
		name string
		cmd  string
		want string
	}{
		{"a. link after --edit-last", "gh pr comment --edit-last https://github.com/x/r/pull/1 -R o/r -b hi", "github:x/r#1"},
		{"a. link after --squash", "gh pr merge --squash https://github.com/x/r/pull/1 -R o/r", "github:x/r#1"},
		{"a. link after -R and --edit-last", "gh pr comment -R o/r --edit-last https://github.com/x/r/pull/1 -b hi", "github:x/r#1"},
		{"a. link after --approve", "gh pr review --approve https://github.com/x/r/pull/1 -R o/r", "github:x/r#1"},
		{"b. env with -u", "env -u FOO gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. sudo with -u", "sudo -u me gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. caffeinate", "caffeinate gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. watch with -n1", "watch -n1 gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. timeout with -s", "timeout -s KILL 5 gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. gh by path", "/opt/homebrew/bin/gh pr comment 1 -R x/r -b hi", "github:x/r#1"},
		{"b. xargs", "echo 1 | xargs gh pr comment -R x/r -b hi", "unknown destination"},
		{"c. alias set", "gh alias set c 'pr comment 1 -R x/r -b hi'; gh c", "gh alias is not checked"},
		{"c. an alias", "gh c", "gh c is not checked"},
		{"c. an extension", "gh extension install x/gh-post", "gh extension is not checked"},
		{"d. issue transfer to another repository", "gh issue transfer 1 x/r -R o/r", "github:x/r"},
		{"d. repo edit of another repository", "gh repo edit x/r --description d", "github:x/r"},
		{"e. xargs gh with the subcommand on its input", "echo 'comment 1 -R x/r -b hi' | xargs gh pr", "unknown destination"},
		{"e. xargs gh with every word on its input", "echo 'pr comment 1 -R x/r -b hi' | xargs gh", "gh without a subcommand"},
		{"e. parallel gh", "parallel gh pr comment {} -R x/r -b hi ::: 1", "unknown destination"},
		{"e. xargs gh api", "echo repos/x/r/issues/1/comments | xargs gh api -f body=hi", "unknown destination"},
		{"f. gh named by a brace", "{gh,} pr comment 1 -R x/r -b hi", "unknown destination"},
		{"f. gh named by a glob", "/opt/homebrew/bin/g? pr comment 1 -R x/r -b hi", "unknown destination"},
		{"f. gh named by an ANSI C string", `$'\x67h' pr comment 1 -R x/r -b hi`, "unknown destination"},
		{"f. gh named by backticks", "`echo gh` pr comment 1 -R x/r -b hi", "unknown destination"},
		{"f. gh named by an expansion behind a wrapper", "timeout 5 ${G}h pr comment 1 -R x/r -b hi", "unknown destination"},
		{"f. gh named by an expansion inside bash -c", `bash -c '${G}h pr comment 1 -R x/r -b hi'`, "unknown destination"},
		{"f. gh named by an expansion inside eval", `eval '$G pr comment 1 -R x/r -b hi'`, "unknown destination"},
		{"g. curl to uploads.github.com", "curl -X POST https://uploads.github.com/repos/x/r/releases/1/assets", "unknown destination"},
		{"g. curl to a Slack webhook", "curl -d '{}' https://hooks.slack.com/services/T/B/x", "unknown destination"},
		{"g. curl to a workspace Slack API", "curl -d text=hi https://acme.slack.com/api/chat.postMessage", "unknown destination"},
		{"g. curl to GitHub Enterprise", "curl -X POST https://ghe.acme.io/api/v3/repos/x/r/issues/1/comments", "unknown destination"},
		{"g. a host split by quotes", `curl -X POST "https://api.git""hub.com/repos/x/r/issues/1/comments"`, "unknown destination"},
		{"h. python3 -c", `python3 -c "import subprocess; subprocess.run(['gh'])"`, "inline python3 program"},
		{"h. node -e", `node -e "require('child_process')"`, "inline node program"},
		{"h. a shell reading a pipe", "echo x | bash", "a shell reading its program from a pipe"},
		{"h. a wrapped shell reading a pipe", "echo x | env sh -s", "a shell reading its program from a pipe"},
		{"i. meetproxy allow", "meetproxy allow 'slack:*'", "changes meetproxy settings"},
		{"i. meetproxy triage command", "meetproxy triage command sh", "changes meetproxy settings"},
		{"i. meetproxy delegation put", `echo '{}' | meetproxy delegation put --data d`, "changes meetproxy settings"},
		{"i. meetproxy resume", "meetproxy resume", "changes meetproxy settings"},
		{"i. meetproxy inbox take", "meetproxy inbox take --data d abcdefabcdef", "changes meetproxy settings"},
		{"i. meetproxy by path", `"/p/bin/meetproxy" --data d allow github:x/*`, "changes meetproxy settings"},
		{"i. meetproxy open of another request", "meetproxy open --data d https://w.slack.com/archives/C9/p1", "slack:C9 is not allowed"},
		{"i. meetproxy open with another target", "meetproxy open https://w.slack.com/archives/C7/p1 --target https://github.com/x/r/pull/1", "github:x/r#1"},
		{"i. meetproxy slack token", "pbpaste | meetproxy slack token --data d", "changes meetproxy settings"},
		{"i. meetproxy slack token behind a boolean flag", "meetproxy --busy slack token", "changes meetproxy settings"},
		{"i. meetproxy slack post elsewhere", "echo hi | meetproxy slack post https://w.slack.com/archives/C9/p1 --session s", "slack:C9 is not allowed"},
		{"i. meetproxy slack post of no link", "echo hi | meetproxy slack post nowhere", "unknown destination"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, check(t, bash(t, tc.cmd), origin, dest.Location{}, false, allow), tc.want)
		})
	}
}

// Merging, closing and deleting are left to the user even on the target
// Approving needs a delegation that lets the review approve
func TestCheck_Acts(t *testing.T) {
	t.Parallel()
	allow := dest.New(t.TempDir())
	require.NoError(t, allow.Add("github:o/*"))
	origin, ok := dest.Parse("https://w.slack.com/archives/C7/p1")
	require.True(t, ok)
	target, ok := dest.Parse("https://github.com/t/r/pull/3")
	require.True(t, ok)
	pr := map[string]any{"owner": "t", "repo": "r", "pull_number": 3}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{"owner": "t", "repo": "r", "pull_number": 3}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	type args struct {
		call       call
		mayApprove bool
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"denies gh pr merge on the target", args{bash(t, "gh pr merge 3 -R t/r --squash"), true}, "a merge of github:t/r#3 is left to the user"},
		{"denies gh pr close on the target", args{bash(t, "gh pr close https://github.com/t/r/pull/3"), true}, "a close of github:t/r#3"},
		{"denies gh issue close in an allowed repository", args{bash(t, "gh issue close 1 -R o/r"), true}, "a close of github:o/r#1"},
		{"denies gh repo archive", args{bash(t, "gh repo archive o/r --yes"), true}, "a close of github:o/r"},
		{"denies gh repo delete", args{bash(t, "gh repo delete o/r --yes"), true}, "a delete of github:o/r"},
		{"denies gh release delete", args{bash(t, "gh release delete v1 -R o/r"), true}, "a delete of github:o/r"},
		{"denies a merge through gh api", args{bash(t, "gh api -X PUT repos/t/r/pulls/3/merge"), true}, "a merge of github:t/r#3"},
		{"denies a delete through gh api", args{bash(t, "gh api -X DELETE repos/o/r/issues/comments/1"), true}, "a delete of github:o/r"},
		{"denies a close through gh api", args{bash(t, "gh api -X PATCH repos/t/r/issues/3 -f state=closed"), true}, "a close of github:t/r#3"},
		{"denies an approval that is not delegated", args{bash(t, "gh pr review 3 -R t/r --approve -b ok"), false}, "approving github:t/r#3 is not delegated"},
		{"denies an approval in a cluster of short flags", args{bash(t, "gh pr review 3 -R t/r -ab ok"), false}, "is not delegated"},
		{"denies an approval through gh api", args{bash(t, "gh api repos/t/r/pulls/3/reviews -f event=APPROVE"), false}, "is not delegated"},
		{"allows a delegated approval on the target", args{bash(t, "gh pr review 3 -R t/r --approve -b ok"), true}, ""},
		{"allows a review comment that is not delegated to approve", args{bash(t, "gh pr review 3 -R t/r --comment -b '-a'"), false}, ""},
		{"denies an MCP approval that is not delegated", args{github(t, "create_pull_request_review", with(map[string]any{"event": "APPROVE"})), false}, "is not delegated"},
		{"allows an MCP review comment", args{github(t, "create_pull_request_review", with(map[string]any{"event": "COMMENT"})), false}, ""},
		{"denies an MCP merge", args{github(t, "merge_pull_request", pr), true}, "a merge of github:t/r#3"},
		{"denies an MCP close", args{github(t, "update_issue", map[string]any{"owner": "t", "repo": "r", "issue_number": 3, "state": "closed"}), true}, "a close of github:t/r#3"},
		{"allows an MCP issue update that keeps it open", args{github(t, "update_issue", map[string]any{"owner": "t", "repo": "r", "issue_number": 3, "title": "x"}), false}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := check(t, tc.args.call, origin, target, tc.args.mayApprove, allow)
			assert.Contains(t, reason, tc.want)
			assert.Equal(t, tc.want == "", reason == "")
		})
	}
}

// The reason the hook gives or an empty string when the call passes
func check(t *testing.T, c call, origin, target dest.Location, mayApprove bool, allow dest.Allow) string {
	t.Helper()
	posts, err := guard.Destinations(c.tool, json.RawMessage(c.input))
	if err != nil {
		return err.Error()
	}
	reason, err := guard.Decide(posts, origin, target, mayApprove, allow.Allowed)
	require.NoError(t, err)
	return reason
}

func TestDecide_AllowListError(t *testing.T) {
	t.Parallel()
	failing := func(dest.Location) (bool, error) { return false, assert.AnError }

	_, err := guard.Decide([]guard.Post{{At: dest.Location{Source: dest.Slack, Name: "C9"}}}, dest.Location{}, dest.Location{}, false, failing)

	assert.ErrorIs(t, err, assert.AnError)
}
