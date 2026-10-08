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
		{"allows meetproxy close and inbox done", bash(t, "meetproxy close && meetproxy inbox done abcdefabcdef"), ""},
		{
			"allows meetproxy commands that only read",
			bash(t, "meetproxy inbox list --data d && meetproxy triage && meetproxy allowed"), "",
		},
		{"allows a meetproxy slack post where the request came from", bash(t, "echo hi | meetproxy slack post https://w.slack.com/archives/C7/p2"), ""},
		{"allows a word that only mentions meetproxy", bash(t, "grep -r meetproxy ."), ""},
		{"allows python without an inline program", bash(t, "python3 -m json.tool a.json"), ""},
		{"allows a shell running a file", bash(t, "bash ./build.sh"), ""},
		{"allows a test and a brace group", bash(t, "[ -f a ] && { ls; }"), ""},
		{"allows a redirection target without a substitution", bash(t, "gh pr view 1 -R x/r > out.txt 2>&1"), ""},
		{"allows a comment with -R on github.com", bash(t, "gh pr comment 1 -R github.com/o/r -b hi"), ""},
		{"allows GH_HOST of github.com", bash(t, "GH_HOST=github.com gh pr comment 1 -R o/r -b hi"), ""},
		{"allows a read on another host", bash(t, "GH_HOST=ghe.acme.io gh pr view 1 -R o/r"), ""},
		{"allows reading a file in the data directory", bash(t, "cat "+dataDir+"/relay/open/s1.json"), ""},
		{"allows meetproxy commands naming the data directory", bash(t, "meetproxy close --data "+dataDir), ""},
		{"allows rm outside the data directory", bash(t, "rm -rf "+dataDir+"-old /tmp/x"), ""},
		{"allows a curl read", bash(t, "curl -fsSL https://gitlab.com/api/v4/projects/1/issues -o out.json"), ""},
		{
			"allows a curl read with a header",
			bash(t, `curl -sH "Authorization: Bearer x" https://api.linear.app/graphql`), "",
		},
		{"allows a curl query sent with -G", bash(t, "curl -G --data-urlencode q=x https://example.com/search"), ""},
		{"allows a curl HEAD", bash(t, "curl -I -X HEAD https://example.com"), ""},
		{"allows a wget read", bash(t, "wget -q -e robots=off -O - https://example.com"), ""},
		{"allows an httpie read with a header and a query", bash(t, "http GET example.com/a Accept:json q==x"), ""},
		{"allows an xh read", bash(t, "xh https://example.com/a q==x"), ""},
		{
			"denies a PowerShell call",
			call{"PowerShell", `{"command":"Get-ChildItem"}`}, "a PowerShell call the guard cannot read",
		},
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
// j. gh inside a redirection target
// k. a repository on another host
// l. a hook run or a file edit that ends the scope by hand
// m. a network write to a host other than Slack or GitHub
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
		{"j. a process substitution read as stdin", "cat < <(gh pr comment 1 -R x/r -b hi)", "github:x/r#1"},
		{"j. a substitution as the output file", `echo x > "$(gh pr comment 1 -R x/r -b hi)"`, "github:x/r#1"},
		{"j. a substitution as the error file", "ls 2>$(gh pr comment 1 -R x/r -b hi)", "github:x/r#1"},
		{"j. a process substitution behind exec", "exec 3> >(gh pr comment 1 -R x/r -b hi)", "github:x/r#1"},
		{"j. backticks as the output file of redirections only", "> `gh pr comment 1 -R x/r -b hi`", "github:x/r#1"},
		{"j. a substitution in the target of a here-string", "cat <<< x > $(gh pr comment 1 -R x/r -b hi)", "github:x/r#1"},
		{"k. -R with another host", "gh pr comment 1 -R ghe.acme.io/o/r -b hi", "a repository on host ghe.acme.io"},
		{"k. --repo with a link to another host", "gh pr comment 1 --repo https://ghe.acme.io/o/r -b hi", "a repository on host ghe.acme.io"},
		{"k. GH_REPO with another host", "GH_REPO=ghe.acme.io/o/r gh pr comment 1 -b hi", "a repository on host ghe.acme.io"},
		{"k. GH_HOST", "GH_HOST=ghe.acme.io gh pr comment 1 -R o/r -b hi", "gh on host ghe.acme.io"},
		{"k. GH_HOST given to env", "env GH_HOST=ghe.acme.io gh issue comment 1 -R o/r -b hi", "gh on host ghe.acme.io"},
		{"k. gh api with --hostname", "gh api --hostname ghe.acme.io repos/o/r/issues/1/comments -f body=hi", "gh on host ghe.acme.io"},
		{"k. gh api with GH_HOST", "GH_HOST=ghe.acme.io gh api repos/o/r/issues/1/comments -f body=hi", "gh on host ghe.acme.io"},
		{"k. an issue transferred to another host", "gh issue transfer 1 ghe.acme.io/o/r -R o/r", "a repository on host ghe.acme.io"},
		{"l. meetproxy hook stop", `echo '{"session_id":"s1"}' | meetproxy hook stop`, "meetproxy hook stop changes meetproxy settings"},
		{"l. meetproxy hook by path with data", `"/p/bin/meetproxy" --data d hook guard`, "meetproxy hook guard changes meetproxy settings"},
		{"l. rm of a scope marker", "rm " + dataDir + "/scope/s1", "a file edit in the meetproxy data directory"},
		{"l. rm of the data directory", "rm -rf " + dataDir, "a file edit in the meetproxy data directory"},
		{"l. mv of an ended relay", "mv " + dataDir + "/relay/ended/s1.json /tmp/x", "a file edit in the meetproxy data directory"},
		{"l. truncate of a request", "truncate -s 0 " + dataDir + "/inbox/abc.json", "a file edit in the meetproxy data directory"},
		{"l. rm behind sudo through a dot path", "sudo rm " + dataDir + "/x/../relay/open/s1.json", "a file edit in the meetproxy data directory"},
		{"l. rm through the data variable", `rm -f "$CLAUDE_PLUGIN_DATA"/scope/*`, "a file edit in the meetproxy data directory"},
		{"l. a redirection into the allow list", `echo '{"allow":["github:*"]}' > ` + dataDir + "/dest.json", "a file edit in the meetproxy data directory"},
		{"l. rm inside bash -c", `bash -c "rm ` + dataDir + `/scope/s1"`, "a file edit in the meetproxy data directory"},
		{
			"m. curl POST to GitLab",
			"curl -X POST https://gitlab.com/api/v4/projects/1/issues/2/notes", "a curl write to gitlab.com",
		},
		{
			"m. curl data to GitLab",
			`curl -sd "body=hi" https://gitlab.com/api/v4/projects/1/issues/2/notes`, "a curl write to gitlab.com",
		},
		{"m. curl -XPOST to Linear", `curl -XPOST https://api.linear.app/graphql`, "a curl write to api.linear.app"},
		{
			"m. curl json to Linear",
			`curl --json '{"query":"mutation"}' https://api.linear.app/graphql`, "a curl write to api.linear.app",
		},
		{
			"m. curl to a Discord webhook",
			`curl -H "Content-Type: application/json" -d '{"content":"hi"}' https://discord.com/api/webhooks/1/x`,
			"a curl write to discord.com",
		},
		{"m. curl form", "curl -F file=@a.txt https://example.com/up", "a curl write to example.com"},
		{"m. curl upload", "curl -T a.txt https://example.com/up", "a curl write to example.com"},
		{"m. curl upload in a cluster", "curl -sT a.txt https://example.com/up", "a curl write to example.com"},
		{"m. curl options from a file", "curl -K req.cfg", "a curl write to a host the guard cannot place"},
		{"m. curl --request DELETE", "curl --request DELETE https://example.com/a/1", "a curl write to example.com"},
		{"m. curl data with -G and POST", "curl -G -d a=1 -X POST https://example.com", "a curl write to example.com"},
		{"m. curl behind sudo", "sudo -u me curl --data-binary @a https://example.com", "a curl write to example.com"},
		{"m. curl.exe", "curl.exe -d a=1 https://example.com", "a curl write to example.com"},
		{"m. curl inside bash -c", `bash -c "curl -d a=1 https://example.com"`, "a curl write to example.com"},
		{"m. curl inside a substitution", `echo "$(curl -d a=1 https://example.com)"`, "a curl write to example.com"},
		{
			"m. wget post data to Discord",
			"wget --post-data='content=hi' https://discord.com/api/webhooks/1/x", "a wget write to discord.com",
		},
		{"m. wget post file", "wget --post-file a.json https://example.com", "a wget write to example.com"},
		{"m. wget method", "wget --method=PUT https://example.com/a", "a wget write to example.com"},
		{"m. wget method set by -e", "wget -e method=POST https://example.com/a", "a wget write to example.com"},
		{
			"m. httpie POST to GitLab",
			"http POST https://gitlab.com/api/v4/projects/1/issues title=x", "a http write to gitlab.com",
		},
		{"m. httpie data item to Linear", "https api.linear.app/graphql query=mutation", "a https write"},
		{"m. httpie json item", "http example.com/a n:=1", "a http write"},
		{"m. httpie file item", "http example.com/a f@a.txt", "a http write"},
		{"m. httpie form", "http -f example.com/a", "a http write"},
		{"m. httpie here document", "http example.com/a <<EOF\n{}\nEOF", "a http write"},
		{"m. xh DELETE", "xh delete https://example.com/a/1", "a xh write to example.com"},
		{"m. xh data to Discord", "xh https://discord.com/api/webhooks/1/x content=hi", "a xh write to discord.com"},
		{"m. pwsh -Command", `pwsh -Command "Invoke-RestMethod -Method Post https://example.com"`, "inline pwsh program"},
		{"m. powershell -c", `powershell.exe -c "iwr https://example.com"`, "inline powershell.exe program"},
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

// Plugin data directory the hook names to the guard
const dataDir = "/plugins/data/meetproxy"

// The reason the hook gives or an empty string when the call passes
func check(t *testing.T, c call, origin, target dest.Location, mayApprove bool, allow dest.Allow) string {
	t.Helper()
	posts, err := guard.Destinations(c.tool, json.RawMessage(c.input), dataDir)
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

// File tools that write in the data directory change meetproxy settings and any other path passes at once
func TestDestinations_FileEdits(t *testing.T) {
	t.Parallel()
	data := "/home/u/.claude/plugins/data/meetproxy-meetproxy"
	tcs := []struct {
		name  string
		tool  string
		input map[string]string
		data  string
		want  error
	}{
		{"a write of the allow list", "Write", map[string]string{"file_path": data + "/allow.json"}, data, guard.ErrSettings},
		{"an edit of a relay", "Edit", map[string]string{"file_path": data + "/relay/open/s1.json"}, data, guard.ErrSettings},
		{"a notebook in the data directory", "NotebookEdit", map[string]string{"notebook_path": data + "/x.ipynb"}, data, guard.ErrSettings},
		{"a path that only shares the prefix", "Write", map[string]string{"file_path": data + "-old/allow.json"}, data, nil},
		{"code elsewhere", "Edit", map[string]string{"file_path": "/home/u/svc/main.go"}, data, nil},
		{"an unknown data directory", "Write", map[string]string{"file_path": data + "/allow.json"}, "", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.input)
			require.NoError(t, err)

			posts, err := guard.Destinations(tc.tool, b, tc.data)

			assert.Empty(t, posts)
			if tc.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
		})
	}
}
