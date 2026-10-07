package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/delegation"
	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

// Creates a repository with one file and returns its root
func gitRepo(t *testing.T, name, file string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, file)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, file), nil, 0o644))
	return root
}

type step struct {
	cmd   string
	args  []string
	stdin string
}

func TestRun(t *testing.T) {
	t.Parallel()
	now := time.Now()
	repo := gitRepo(t, "svc", "pkg/alloc.go")
	inRepo := filepath.Join(repo, "pkg", "alloc.go")
	link := "https://w.slack.com/archives/C1/p1"
	later := "https://w.slack.com/archives/C1/p2"
	id := inbox.IdOf(link)
	thread := inbox.IdOf("w.slack.com/C1/1")
	allowed := step{"allow", []string{"slack:C1"}, ""}
	queued := step{"inbox", []string{"add", link}, ""}
	taken := step{"inbox", []string{"take", id}, ""}
	inThread := func(link, ts, text string) step {
		return step{"inbox", []string{"add", link, "--thread", "w.slack.com/C1/1", "--ts", ts, "--summary", text}, ""}
	}
	prDelegation := `{"id":"pr","when":"mention","link":"github-pr","do":"review"}`
	builtIn := `{"id":"default-review","when":"mention","link":"github-pr","do":"review","approve":"self"},` +
		`{"id":"default-review-request","when":"review-request","do":"review","approve":"self"},` +
		`{"id":"default-dm","when":"dm","do":"answer"},` +
		`{"id":"default-own-pr","when":"own-pr","do":"answer"},` +
		`{"id":"default","when":"mention","do":"answer"}]`
	recorded := []step{
		{"open", []string{"o"}, ""},
		{"close", []string{"--topic", "할당 원인", "--keywords", "할당", "--paths", inRepo}, ""},
	}
	long := strings.Repeat("가", 250)

	type want struct {
		code   int
		out    string
		failed bool
	}
	tcs := []struct {
		name    string
		session string
		noData  bool
		setup   []step
		run     step
		want    want
	}{
		{"usage error without --data", "s1", true, nil, step{"locate", []string{"x"}, ""}, want{exitUsage, "", true}},
		{"usage error for open without a session", "", false, nil, step{"open", []string{"o"}, ""}, want{exitUsage, "", true}},
		{"usage error for an unknown command", "s1", false, nil, step{"nope", nil, ""}, want{exitUsage, "", true}},
		{"dest denies without an allow list", "s1", false, nil, step{"dest", []string{"slack:C1"}, ""}, want{1, "denied", false}},
		{
			"dest allows a link after allow", "s1", false,
			[]step{{"allow", []string{"slack:C1"}, ""}},
			step{"dest", []string{"https://w.slack.com/archives/C1/p1"}, ""},
			want{0, "allowed", false},
		},
		{
			"allow rejects a pattern without a normal form", "s1", false, nil,
			step{"allow", []string{"https://example.com"}, ""}, want{1, "", true},
		},
		{
			"usage error for close without --topic", "s1", false,
			[]step{{"open", []string{"o"}, ""}}, step{"close", nil, ""}, want{exitUsage, "", true},
		},
		{"close fails without an open relay", "s1", false, nil, step{"close", []string{"--topic", "t"}, ""}, want{1, "", true}},
		{
			"close skips evidence outside a repository and still closes", "s1", false,
			[]step{{"open", []string{"o"}, ""}, {"close", []string{"--topic", "t", "--paths", "/nowhere/x.go"}, ""}},
			step{"close", []string{"--topic", "t"}, ""},
			want{1, "", true},
		},
		{
			"locate finds what close recorded", "s1", false,
			recorded,
			step{"locate", []string{"할당이"}, ""},
			want{0, "1\tsvc\t" + inRepo + "\t할당 원인", false},
		},
		{"route is gone", "s1", false, recorded, step{"route", []string{"할당이"}, ""}, want{exitUsage, "", true}},
		{"usage error for open with two origins", "s1", false, nil, step{"open", []string{"o", "p"}, ""}, want{exitUsage, "", true}},
		{
			"usage error for close with an argument", "s1", false, []step{{"open", []string{"o"}, ""}},
			step{"close", []string{"foo", "--topic", "t"}, ""}, want{exitUsage, "", true},
		},
		{"usage error for allowed with an argument", "s1", false, nil, step{"allowed", []string{"x"}, ""}, want{exitUsage, "", true}},
		{
			"usage error for a flag that does not parse", "s1", false, nil,
			step{"locate", []string{"x", "--limit", "many"}, ""}, want{exitUsage, "", true},
		},
		{"usage error for an unknown flag", "s1", false, nil, step{"locate", []string{"x", "--nope", "1"}, ""}, want{exitUsage, "", true}},
		{
			"usage error for a flag the command does not read", "s1", false, nil,
			step{"allowed", []string{"--topic", "t"}, ""}, want{exitUsage, "", true},
		},
		{"protocol needs no data directory", "s1", true, nil, step{"protocol", nil, ""}, want{0, strconv.Itoa(protocol), false}},
		{"usage error for tick with --cwd", "s1", false, nil, step{"tick", []string{"--cwd", "/tmp"}, ""}, want{exitUsage, "", true}},
		{
			"tick prints an empty list when nothing waits", "s1", false, nil, step{"tick", nil, ""},
			want{0, `{"protocol":` + strconv.Itoa(protocol) + `,"waiting":[],"slack":{"token":false},"watch":[],"acks":[]}`, false},
		},
		{"allowed lists the patterns", "s1", false, []step{allowed}, step{"allowed", nil, ""}, want{0, "slack:C1", false}},
		{
			"can-post allows where the open relay came from", "s1", false,
			[]step{{"open", []string{"https://w.slack.com/archives/C7/p1"}, ""}},
			step{"can-post", []string{"https://w.slack.com/archives/C7/p2"}, ""}, want{0, "allowed", false},
		},
		{
			"can-post denies another place without an allow list entry", "s1", false,
			[]step{{"open", []string{"https://w.slack.com/archives/C7/p1"}, ""}},
			step{"can-post", []string{"https://w.slack.com/archives/C8/p2"}, ""}, want{1, "denied", false},
		},
		{
			"can-post allows an allowed place with no relay", "s1", false, []step{allowed},
			step{"can-post", []string{"slack:C1"}, ""}, want{0, "allowed", false},
		},
		{"inbox add queues an open request", "s1", false, []step{allowed}, queued, want{0, id + "\topen\tnew", false}},
		{"inbox add reports a link seen before", "s1", false, []step{allowed, queued}, queued, want{0, id + "\topen\tseen", false}},
		{"inbox add takes a channel outside the allow list", "s1", false, nil, queued, want{0, id + "\topen\tnew", false}},
		{
			"usage error for inbox add with a verdict", "s1", false, nil,
			step{"inbox", []string{"add", link, "--verdict", "handle"}, ""}, want{exitUsage, "", true},
		},
		{
			"usage error for inbox add with a place", "s1", false, nil,
			step{"inbox", []string{"add", link, "--place", "/w/svc"}, ""}, want{exitUsage, "", true},
		},
		{
			"inbox add groups the messages of a thread into one request", "s1", false,
			[]step{inThread(link, "1800000000.0001", "first")},
			inThread(later, "1800000000.0002", "second"), want{0, thread + "\topen\tseen", false},
		},
		{
			"inbox list shows the first link and the newest summary of a thread", "s1", false,
			[]step{inThread(link, "1800000000.0001", "first"), inThread(later, "1800000000.0002", "second")},
			step{"inbox", []string{"list"}, ""}, want{0, thread + "\topen\t-\t-\t" + link + "\tsecond", false},
		},
		{
			"inbox list keeps the summary of a newer message over an older one", "s1", false,
			[]step{inThread(later, "1800000000.0002", "second"), inThread(link, "1800000000.0001", "first")},
			step{"inbox", []string{"list"}, ""}, want{0, thread + "\topen\t-\t-\t" + later + "\tsecond", false},
		},
		{
			"inbox add keeps the first non empty line of the summary with spaces collapsed", "s1", false,
			[]step{{"inbox", []string{"add", link, "--summary", "\n  where   is\talloc \nsecond line"}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\topen\t-\t-\t" + link + "\twhere is alloc", false},
		},
		{
			"inbox add cuts a long summary", "s1", false,
			[]step{{"inbox", []string{"add", link, "--summary", long}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\topen\t-\t-\t" + link + "\t" + long[:199*3] + "…", false},
		},
		{
			"inbox list prints the source and the author", "s1", false,
			[]step{{"inbox", []string{"add", link, "--source", "slack", "--author", "Kai", "--summary", "hi"}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\topen\tslack\tKai\t" + link + "\thi", false},
		},
		{
			"inbox add marks a taken request asked again", "s1", false,
			[]step{inThread(link, "1800000000.0001", "first"), {"inbox", []string{"take", thread}, ""}},
			inThread(later, "1800000000.0002", "second"), want{0, thread + "\ttaken\tseen", false},
		},
		{
			"inbox done opens a request asked again during the take", "s1", false,
			[]step{
				inThread(link, "1800000000.0001", "first"), {"inbox", []string{"take", thread}, ""},
				inThread(later, "1800000000.0002", "second"), {"inbox", []string{"done", thread}, ""},
			},
			step{"inbox", []string{"list"}, ""}, want{0, thread + "\topen\t-\t-\t" + link + "\tsecond", false},
		},
		{
			"inbox cursor moves with the newest request", "s1", false,
			[]step{allowed, {"inbox", []string{"add", link, "--ts", "1800000000.0001"}, ""}},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0001", false},
		},
		{
			"inbox add moves the cursor for a link seen before", "s1", false,
			[]step{
				{"inbox", []string{"add", link, "--ts", "1800000000.0001"}, ""},
				{"inbox", []string{"add", link, "--ts", "1800000000.0002"}, ""},
			},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0002", false},
		},
		{
			"inbox add opens a done request again with a new timestamp", "s1", false,
			[]step{{"inbox", []string{"add", link, "--ts", "1800000000.0001"}, ""}, {"inbox", []string{"done", id}, ""}},
			step{"inbox", []string{"add", link, "--ts", "1800000000.0002"}, ""}, want{0, id + "\topen\tnew", false},
		},
		{
			"inbox advance moves the cursor without a request", "s1", false,
			[]step{{"inbox", []string{"advance", "1800000000.0002"}, ""}},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0002", false},
		},
		{"inbox claim is gone", "s1", false, []step{queued}, step{"inbox", []string{"claim", id}, ""}, want{exitUsage, "", true}},
		{"inbox ask is gone", "s1", false, []step{queued}, step{"inbox", []string{"ask", id}, ""}, want{exitUsage, "", true}},
		{"inbox waiting is gone", "s1", false, []step{queued}, step{"inbox", []string{"waiting"}, ""}, want{exitUsage, "", true}},
		{"map plan is gone", "s1", false, nil, step{"map", []string{"plan"}, "svc"}, want{exitUsage, "", true}},
		{"slack shared is gone", "s1", false, nil, step{"slack", []string{"shared", "C1"}, ""}, want{exitUsage, "", true}},
		{
			"inbox list hides a done request", "s1", false,
			[]step{queued, {"inbox", []string{"add", later}, ""}, {"inbox", []string{"done", id}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, inbox.IdOf(later) + "\topen\t-\t-\t" + later + "\t-", false},
		},
		{
			"inbox list shows a take as taken", "s1", false, []step{queued, taken},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\ttaken\t-\t-\t" + link + "\t-", false},
		},
		{
			"inbox list shows a held request whose time came as open", "s1", false,
			[]step{queued, {"inbox", []string{"hold", id, "--until", "2000-01-01T00:00:00Z"}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\topen\t-\t-\t" + link + "\t-", false},
		},
		{
			"inbox take prints link, task, target, approve, depth and flags", "s1", false, []step{allowed, queued},
			taken, want{0, link + "\tanswer\t-\tno\tquick\t-", false},
		},
		{
			"inbox take prints the depth and the flags of a follow-up", "s1", false,
			[]step{{"inbox", []string{"add", link, "--depth", "deep", "--followup", "yes", "--correction", "yes"}, ""}},
			taken, want{0, link + "\tanswer\t-\tno\tdeep\tfollowup,correction", false},
		},
		{
			"inbox take fails while another session holds the request", "s2", false,
			[]step{queued, {"inbox", []string{"take", id, "--session", "s1"}, ""}}, taken, want{1, "", true},
		},
		{
			"inbox take fails on a done request", "s1", false,
			[]step{queued, {"inbox", []string{"done", id}, ""}}, taken, want{1, "", true},
		},
		{
			"inbox add keeps the task of a delegation and lets a review approve the user's request", "s1", false,
			[]step{allowed, {"delegation", []string{"put"}, prDelegation}, {"inbox", []string{
				"add", link, "--delegation", "pr", "--target", "https://github.com/o/r/pull/1", "--self", "yes",
			}, ""}},
			taken, want{0, link + "\treview\thttps://github.com/o/r/pull/1\tyes\tquick\t-", false},
		},
		{
			"inbox cursor keeps one per key", "s1", false,
			[]step{{"inbox", []string{"advance", "1800000000.1", "--key", "alerts"}, ""}},
			step{"inbox", []string{"cursor", "--key", "alerts"}, ""}, want{0, "1800000000.1", false},
		},
		{
			"delegation lists the built in delegations last", "s1", false, nil, step{"delegation", nil, ""},
			want{0, `[` + builtIn, false},
		},
		{
			"delegation put adds a channel delegation", "s1", false,
			[]step{{"delegation", []string{"put"}, `{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com","do":"investigate"}`}},
			step{"delegation", nil, ""},
			want{0, `[{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com","do":"investigate"},` + builtIn, false},
		},
		{
			"delegation match picks a review for a pull request link", "s1", false, []step{{"delegation", []string{"put"}, prDelegation}},
			step{"delegation", []string{"match", "mention"}, `{"text":"<@U1> review https://github.com/o/svc/pull/7 please","from":"U2"}`},
			want{0, `{"delegation":"pr","task":"review","triage":false,"target":"https://github.com/o/svc/pull/7",` +
				`"depth":"quick","digest":"d4893200715e"}`, false},
		},
		{
			"delegation match reviews a requested pull request", "s1", false, nil,
			step{"delegation", []string{"match", "review-request"}, `{"link":"https://github.com/o/svc/pull/9","text":"Review requested: fix","from":"a"}`},
			want{0, `{"delegation":"default-review-request","task":"review","triage":false,"target":"https://github.com/o/svc/pull/9",` +
				`"depth":"quick","digest":"a7387bdfe8ee"}`, false},
		},
		{
			"delegation match falls back to the default for a question", "s1", false, []step{{"delegation", []string{"put"}, prDelegation}},
			step{"delegation", []string{"match", "mention"}, `{"text":"where is alloc","from":"U2"}`},
			want{0, `{"delegation":"default","task":"answer","triage":true,"depth":"quick","digest":"f6c5abd79fc4"}`, false},
		},
		{
			"delegation match reads a deep request", "s1", false, nil,
			step{"delegation", []string{"match", "dm"}, `{"text":"[deep] why is alloc slow","from":"U2"}`},
			want{0, `{"delegation":"default-dm","task":"answer","triage":true,"depth":"deep","digest":"` +
				delegation.Digest("[deep] why is alloc slow") + `"}`, false},
		},
		{
			"delegation match of a follow-up picks the delegation by id and triages it", "s1", false,
			[]step{{"delegation", []string{"put"}, prDelegation}},
			step{"delegation", []string{"match", "pr", "--followup", "yes"}, `{"text":"thanks","from":"U2"}`},
			want{0, `{"delegation":"pr","task":"review","triage":true,"depth":"quick","digest":"` + delegation.Digest("thanks") + `"}`, false},
		},
		{
			"delegation match prints nothing when the author of a channel delegation differs", "s1", false,
			[]step{{"delegation", []string{"put"}, `{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com",` +
				`"from":["datadog"],"words":["Triggered"],"do":"investigate"}`}},
			step{"delegation", []string{"match", "alerts"}, `{"text":"Recovered: cpu","from":"U9","author":"Datadog"}`},
			want{0, "", false},
		},
		{
			"delegation remove drops a delegation", "s1", false,
			[]step{{"delegation", []string{"put"}, prDelegation}, {"delegation", []string{"remove", "pr"}, ""}},
			step{"delegation", nil, ""}, want{0, `[` + builtIn, false},
		},
		{
			"inbox question settles a taken request and closes its relay", "s1", false,
			[]step{allowed, queued, taken, {"open", []string{link}, ""}, {"inbox", []string{"question", id}, ""}},
			step{"close", []string{"--topic", "t"}, ""}, want{1, "", true},
		},
		{
			"inbox question lists the request as waiting for the requester", "s1", false,
			[]step{queued, taken, {"inbox", []string{"question", id}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\tquestion\t-\t-\t" + link + "\t-", false},
		},
		{
			"usage error for inbox take without a session", "", false, []step{allowed, queued},
			taken, want{exitUsage, "", true},
		},
		{"usage error for inbox without a command", "s1", false, nil, step{"inbox", nil, ""}, want{exitUsage, "", true}},
		{"usage error for an unknown inbox command", "s1", false, nil, step{"inbox", []string{"drop"}, ""}, want{exitUsage, "", true}},
		{"inbox add fails while paused", "s1", false, []step{allowed, {"pause", nil, ""}}, queued, want{1, "", true}},
		{
			"inbox add works after resume", "s1", false, []step{allowed, {"pause", nil, ""}, {"resume", nil, ""}},
			queued, want{0, id + "\topen\tnew", false},
		},
		{
			"close marks the request it relayed done", "s1", false,
			[]step{allowed, queued, taken, {"open", []string{link}, ""}, {"close", []string{"--topic", "t", "--paths", inRepo}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, "", false},
		},
		{
			"close settles the request of a thread found by its first link", "s1", false,
			[]step{
				inThread(link, "1800000000.0001", "first"), inThread(later, "1800000000.0002", "second"),
				{"inbox", []string{"take", thread}, ""}, {"open", []string{link}, ""}, {"close", []string{"--topic", "t"}, ""},
			},
			step{"inbox", []string{"list"}, ""}, want{0, "", false},
		},
		{
			"close of a relay opened by hand for an unknown link settles nothing", "s1", false,
			[]step{queued, {"open", []string{"https://w.slack.com/archives/C1/p9"}, ""}, {"close", []string{"--topic", "t"}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\topen\t-\t-\t" + link + "\t-", false},
		},
		{"triage defaults to claude", "s1", false, nil, step{"triage", nil, ""}, want{0, "claude", false}},
		{
			"triage keeps a command line", "s1", false, []step{{"triage", []string{"command", "laya", "judge"}, ""}},
			step{"triage", nil, ""}, want{0, "command laya judge", false},
		},
		{"triage refuses an unknown engine", "s1", false, nil, step{"triage", []string{"gemini"}, ""}, want{1, "", true}},
		{"usage error for a second engine", "s1", false, nil, step{"triage", []string{"claude", "extra"}, ""}, want{exitUsage, "", true}},
		{
			"usage error for triage command without a command line", "s1", false, nil,
			step{"triage", []string{"command"}, ""}, want{exitUsage, "", true},
		},
		{
			"usage error for triage run on input that is no JSON", "s1", false, nil,
			step{"triage", []string{"run"}, "text"}, want{exitUsage, "", true},
		},
		{
			"usage error for triage prompt on input that is no JSON", "s1", false, nil,
			step{"triage", []string{"prompt"}, "text"}, want{exitUsage, "", true},
		},
		{
			"triage parse reads a verdict from a reply", "s1", false, nil,
			step{"triage", []string{"parse"}, "sure {\"verdict\":\"keep\",\"reason\":\"code question\"}"},
			want{0, `{"verdict":"keep","reason":"code question"}`, false},
		},
		{
			"triage parse keeps a verdict that is gone", "s1", false, nil,
			step{"triage", []string{"parse"}, `{"verdict":"handle","reason":"r"}`},
			want{0, `{"verdict":"keep","reason":"triage failed: unknown verdict \"handle\""}`, false},
		},
		{
			"triage run uses a command engine", "s1", false,
			[]step{{"triage", []string{"command", `echo '{"verdict":"ignore","reason":"fyi"}'`}, ""}},
			step{"triage", []string{"run"}, `{"text":"fyi"}`}, want{0, `{"verdict":"ignore","reason":"fyi"}`, false},
		},
		{
			"triage run keeps the message when the engine fails", "s1", false, []step{{"triage", []string{"command", "exit 3"}, ""}},
			step{"triage", []string{"run"}, `{"text":"x"}`}, want{0, `{"verdict":"keep","reason":"triage failed: exit 3: exit status 3 "}`, false},
		},
		{
			"triage keeps a long flag inside a command line", "s1", false,
			[]step{{"triage", []string{"command", "laya", "--limit", "5"}, ""}},
			step{"triage", nil, ""}, want{0, "command laya --limit 5", false},
		},
		{
			"triage keeps a short flag inside a command line", "s1", false, []step{{"triage", []string{"command", "laya", "-p"}, ""}},
			step{"triage", nil, ""}, want{0, "command laya -p", false},
		},
		{
			"open stores the target a post may go to", "s1", false,
			[]step{{"open", []string{"o", "--target", "https://github.com/o/r/pull/1"}, ""}},
			step{"can-post", []string{"https://github.com/o/r/pull/1"}, ""}, want{0, "allowed", false},
		},
		{"can-post fails on a link that names no place", "s1", false, nil, step{"can-post", []string{"notalink"}, ""}, want{1, "", true}},
		{"inbox add fails on a link that names no place", "s1", false, nil, step{"inbox", []string{"add", "notalink"}, ""}, want{1, "", true}},
		{
			"inbox add fails on a timestamp the cursor refuses", "s1", false, nil,
			step{"inbox", []string{"add", link, "--ts", "abc"}, ""}, want{1, "", true},
		},
		{
			"inbox add fails on an unknown delegation", "s1", false, nil,
			step{"inbox", []string{"add", link, "--delegation", "nope"}, ""}, want{1, "", true},
		},
		{
			"usage error for inbox add with an unknown follow-up", "s1", false, nil,
			step{"inbox", []string{"add", link, "--followup", "maybe"}, ""}, want{exitUsage, "", true},
		},
		{
			"usage error for inbox add with an unknown depth", "s1", false, nil,
			step{"inbox", []string{"add", link, "--depth", "wide"}, ""}, want{exitUsage, "", true},
		},
		{
			"inbox hold puts a taken request off", "s1", false,
			[]step{queued, taken, {"inbox", []string{"hold", id}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\theld\t-\t-\t" + link + "\t-", false},
		},
		{
			"inbox hold puts an open request off", "s1", false,
			[]step{queued, {"inbox", []string{"hold", id, "--until", "1h"}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\theld\t-\t-\t" + link + "\t-", false},
		},
		{
			"inbox done keeps the relay of another request open", "s1", false,
			[]step{queued, {"open", []string{"https://w.slack.com/archives/C7/p1"}, ""}, {"inbox", []string{"done", id}, ""}},
			step{"can-post", []string{"https://w.slack.com/archives/C7/p2"}, ""}, want{0, "allowed", false},
		},
		{"usage error for inbox take without an id", "s1", false, nil, step{"inbox", []string{"take"}, ""}, want{exitUsage, "", true}},
		{"usage error for inbox list with an argument", "s1", false, nil, step{"inbox", []string{"list", "x"}, ""}, want{exitUsage, "", true}},
		{"usage error for delegation match without a key", "s1", false, nil, step{"delegation", []string{"match"}, ""}, want{exitUsage, "", true}},
		{
			"usage error for delegation match on a message that is no JSON", "s1", false, nil,
			step{"delegation", []string{"match", "mention"}, "text"}, want{exitUsage, "", true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			for _, s := range tc.setup {
				code, err := run(s.cmd, slices.Concat(data, s.args), tc.session, now, strings.NewReader(s.stdin), &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			runArgs := tc.run.args
			if !tc.noData {
				runArgs = slices.Concat(data, runArgs)
			}
			var out bytes.Buffer
			code, err := run(tc.run.cmd, runArgs, tc.session, now, strings.NewReader(tc.run.stdin), &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
		})
	}
}

func TestRun_BrokenState(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		code   int
		out    string
		failed bool
	}
	tcs := []struct {
		name   string
		broken string
		run    step
		want   want
	}{
		{
			"delegation match fails when the delegations cannot be read", "delegations.json",
			step{"delegation", []string{"match", "mention"}, `{"text":"where is alloc"}`}, want{1, "", true},
		},
		{
			"can-post fails when the open relay cannot be read", filepath.Join("relay", "open", "s1.json"),
			step{"can-post", []string{"slack:C1"}, ""}, want{1, "", true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			file := filepath.Join(data, tc.broken)
			require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
			require.NoError(t, os.WriteFile(file, []byte("{"), 0o644))
			var out bytes.Buffer
			code, err := run(tc.run.cmd, append([]string{"--data", data}, tc.run.args...), "s1", now, strings.NewReader(tc.run.stdin), &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
		})
	}
}

// A command that fails part way leaves state the same command finishes when it runs again
func TestRun_Retry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	link := "https://w.slack.com/archives/C1/p1"
	id := inbox.IdOf(link)
	evidence := gitRepo(t, "svc", "a.go")
	taken := []step{
		{"inbox", []string{"add", link}, ""},
		{"inbox", []string{"take", id}, ""},
		{"open", []string{link}, ""},
	}
	closing := step{"close", []string{"--topic", "t", "--paths", evidence}, ""}
	listed := step{"inbox", []string{"list"}, ""}
	located := step{"locate", []string{"t"}, ""}
	closedRelays := filepath.Join("relay", "closed.jsonl")

	tcs := []struct {
		name string
		// Made a directory for the first run so writing or reading it fails
		blocked string
		run     step
		check   step
		want    string
	}{
		{"close after the request failed to settle", filepath.Join("inbox", id+".json"), closing, listed, ""},
		{"close after the location map failed", "map.jsonl", closing, located, "1\tsvc\t" + evidence + "\tt"},
		{"close after the relay failed to close", closedRelays, closing, located, "1\tsvc\t" + evidence + "\tt"},
		{
			"question after the relay failed to close", closedRelays, step{"inbox", []string{"question", id}, ""}, listed,
			id + "\tquestion\t-\t-\t" + link + "\t-",
		},
		{"hold after the relay failed to close", closedRelays, step{"inbox", []string{"hold", id}, ""}, listed, id + "\theld\t-\t-\t" + link + "\t-"},
		{"done after the relay failed to close", closedRelays, step{"inbox", []string{"done", id}, ""}, listed, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			runStep := func(s step, out *bytes.Buffer) (int, error) {
				return run(s.cmd, slices.Concat(data, s.args), "s1", now, strings.NewReader(s.stdin), out)
			}
			for _, s := range taken {
				code, err := runStep(s, &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			blocked := filepath.Join(data[1], tc.blocked)
			require.NoError(t, os.RemoveAll(blocked))
			require.NoError(t, os.Mkdir(blocked, 0o755))
			code, err := runStep(tc.run, &bytes.Buffer{})
			require.Error(t, err)
			require.Equal(t, exitFailed, code)

			require.NoError(t, os.Remove(blocked))
			code, err = runStep(tc.run, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, 0, code)
			var out bytes.Buffer
			_, err = runStep(tc.check, &out)
			require.NoError(t, err)
			assert.Equal(t, tc.want, strings.TrimSpace(out.String()))
			_, err = relay.New(data[1]).Current("s1")
			assert.ErrorIs(t, err, relay.ErrNoOpen)
		})
	}
}

func TestRun_Tick(t *testing.T) {
	t.Parallel()
	now := time.Unix(1893456000, 0)
	opened, held := "https://w.slack.com/archives/C1/p1", "https://w.slack.com/archives/C1/p2"
	taken, done := "https://w.slack.com/archives/C1/p3", "https://w.slack.com/archives/C1/p4"
	until := now.Add(time.Hour)
	data := []string{"--data", t.TempDir()}
	steps := []step{
		{"inbox", []string{"add", opened, "--source", "slack", "--author", "Kai", "--summary", "where is alloc\nmore"}, ""},
		{"inbox", []string{"add", held}, ""},
		{"inbox", []string{"add", taken}, ""},
		{"inbox", []string{"add", done}, ""},
		{"inbox", []string{"hold", inbox.IdOf(held), "--until", until.Format(time.RFC3339)}, ""},
		{"inbox", []string{"take", inbox.IdOf(taken)}, ""},
		{"inbox", []string{"done", inbox.IdOf(done)}, ""},
	}
	for i, s := range steps {
		code, err := run(s.cmd, slices.Concat(data, s.args), "s1", now.Add(time.Duration(i)*time.Second), nil, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, 0, code)
	}
	at := func(i int) int64 { return now.Add(time.Duration(i) * time.Second).Unix() }
	openRow := fmt.Sprintf(`{"id":%q,"status":"open","open":true,"link":%q,"source":"slack","author":"Kai",`+
		`"summary":"where is alloc","task":"answer","added":%d}`, inbox.IdOf(opened), opened, at(0))
	heldRow := func(open bool) string {
		return fmt.Sprintf(`{"id":%q,"status":"held","open":%t,"link":%q,"task":"answer","added":%d,"until":%d}`,
			inbox.IdOf(held), open, held, at(1), until.Unix())
	}
	releasedRow := fmt.Sprintf(`{"id":%q,"status":"open","open":true,"link":%q,"task":"answer","added":%d}`,
		inbox.IdOf(taken), taken, at(2))
	// Every request not done owes eyes newest first and the done one has no post
	acks := fmt.Sprintf(`[{"id":%q,"link":%q,"react":"eyes"},{"id":%q,"link":%q,"react":"eyes"},{"id":%q,"link":%q,"react":"eyes"}]`,
		inbox.IdOf(taken), taken, inbox.IdOf(held), held, inbox.IdOf(opened), opened)
	body := func(waiting []string, acks string) string {
		return fmt.Sprintf(`{"protocol":%d,"waiting":[%s],"slack":{"token":false},"watch":[],"acks":%s}`,
			protocol, strings.Join(waiting, ","), acks)
	}

	tcs := []struct {
		name  string
		after time.Duration
		want  string
	}{
		{"a held request waits closed and a take is left out", time.Minute, body([]string{openRow, heldRow(false)}, acks)},
		{"a held request whose time came is open", 2 * time.Hour, body([]string{openRow, heldRow(true)}, acks)},
		{"a take kept past a day opens again and no reaction is owed", 25 * time.Hour, body([]string{openRow, heldRow(true), releasedRow}, "[]")},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			code, err := run("tick", data, "s1", now.Add(tc.after), nil, &out)
			require.NoError(t, err)
			require.Equal(t, 0, code)
			assert.JSONEq(t, tc.want, out.String())
		})
	}
}

func TestRun_InboxListLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	older, newer := "https://w.slack.com/archives/C1/p1", "https://w.slack.com/archives/C1/p2"
	line := func(link string) string { return inbox.IdOf(link) + "\topen\t-\t-\t" + link + "\t-" }
	tcs := []struct {
		name  string
		limit string
		want  []string
	}{
		{"one keeps the newest request", "1", []string{line(newer)}},
		{"more than the queue lists every request", "5", []string{line(newer), line(older)}},
		{"zero lists every request", "0", []string{line(newer), line(older)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			for i, link := range []string{older, newer} {
				at := now.Add(time.Duration(i) * time.Second)
				code, err := run("inbox", slices.Concat(data, []string{"add", link}), "s1", at, nil, &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			var out bytes.Buffer
			code, err := run("inbox", slices.Concat(data, []string{"list", "--limit", tc.limit}), "s1", now, nil, &out)
			require.NoError(t, err)
			require.Equal(t, 0, code)
			assert.Equal(t, tc.want, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"))
		})
	}
}

// Not parallel since the data directory comes from the environment
func TestRun_Root(t *testing.T) {
	config := t.TempDir()
	installed := filepath.Join(config, "plugins", "cache", "jeon.tools", "meetproxy", "0.2.0")
	type args struct {
		config string
		home   string
		root   string
	}
	type want struct {
		code     int
		failed   bool
		patterns []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an installed copy keeps its data under the config directory", args{config, "", installed}, want{0, false, []string{"slack:C1"}}},
		{"a directory without a manifest is a usage error", args{config, "", t.TempDir()}, want{exitUsage, true, nil}},
		{"no config directory and no home is a usage error", args{"", "", installed}, want{exitUsage, true, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tc.args.config)
			t.Setenv("HOME", tc.args.home)
			data := filepath.Join(config, "plugins", "data", "meetproxy-jeon-tools")
			require.NoError(t, os.RemoveAll(data))
			code, err := run("allow", []string{"slack:C1", "--root", tc.args.root}, "s1", time.Now(), nil, &bytes.Buffer{})
			patterns, perr := dest.New(data).Patterns()
			require.NoError(t, perr)
			assert.Equal(t, tc.want, want{code, err != nil, patterns})
		})
	}
}

func TestReorder(t *testing.T) {
	t.Parallel()
	type args struct {
		cmd  string
		args []string
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"moves a double dash flag", args{"locate", []string{"x", "--data", "d"}}, []string{"--data", "d", "x"}},
		{"moves a single dash flag", args{"locate", []string{"x", "-data", "d"}}, []string{"-data", "d", "x"}},
		{"keeps an equals flag whole", args{"locate", []string{"--data=d", "x"}}, []string{"--data=d", "x"}},
		{"keeps a lone dash as an argument", args{"locate", []string{"-", "x"}}, []string{"-", "x"}},
		{
			"keeps every word after a double dash", args{"locate", []string{"x", "--data", "d", "--", "-p", "--limit", "5"}},
			[]string{"--data", "d", "--", "x", "-p", "--limit", "5"},
		},
		{
			"keeps every word of a triage command line", args{"triage", []string{"--data", "d", "command", "laya", "--limit", "5", "-p"}},
			[]string{"--data", "d", "command", "laya", "--limit", "5", "-p"},
		},
		{"moves flags after another triage word", args{"triage", []string{"claude", "--data", "d"}}, []string{"--data", "d", "claude"}},
		{"keeps a command word of another command", args{"locate", []string{"command", "--data", "d"}}, []string{"--data", "d", "command"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args.cmd, tc.args.args))
		})
	}
}

func TestSummary(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("가", summaryRunes)
	tcs := []struct {
		name string
		text string
		want string
	}{
		{"keeps a short line", "where is alloc", "where is alloc"},
		{"keeps the first line that is not empty", "\n \t\nfirst\nsecond", "first"},
		{"collapses whitespace", "  a \t b   c  ", "a b c"},
		{"keeps a line of exactly the limit", exact, exact},
		{"cuts a longer line with an ellipsis", exact + "나", strings.Repeat("가", summaryRunes-1) + "…"},
		{"is empty for blank text", " \n\t", ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, summary(tc.text))
		})
	}
}
