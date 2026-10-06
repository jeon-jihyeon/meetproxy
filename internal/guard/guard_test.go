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
		{"treats gh without a subcommand as reading", bash(t, "gh --version"), ""},
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
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason := check(t, tc.call, origin, target, allow)
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
		{"b. xargs", "echo 1 | xargs gh pr comment -R x/r -b hi", "github:x/r"},
		{"c. alias set", "gh alias set c 'pr comment 1 -R x/r -b hi'; gh c", "gh alias is not checked"},
		{"c. an alias", "gh c", "gh c is not checked"},
		{"c. an extension", "gh extension install x/gh-post", "gh extension is not checked"},
		{"d. issue transfer to another repository", "gh issue transfer 1 x/r -R o/r", "github:x/r"},
		{"d. repo edit of another repository", "gh repo edit x/r --description d", "github:x/r"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, check(t, bash(t, tc.cmd), origin, dest.Location{}, allow), tc.want)
		})
	}
}

// The reason the hook gives or an empty string when the call passes
func check(t *testing.T, c call, origin, target dest.Location, allow dest.Allow) string {
	t.Helper()
	locs, err := guard.Destinations(c.tool, json.RawMessage(c.input))
	if err != nil {
		return err.Error()
	}
	reason, err := guard.Decide(locs, origin, target, allow.Allowed)
	require.NoError(t, err)
	return reason
}

func TestDecide_AllowListError(t *testing.T) {
	t.Parallel()
	failing := func(dest.Location) (bool, error) { return false, assert.AnError }

	_, err := guard.Decide([]dest.Location{{Source: dest.Slack, Name: "C9"}}, dest.Location{}, dest.Location{}, failing)

	assert.ErrorIs(t, err, assert.AnError)
}
