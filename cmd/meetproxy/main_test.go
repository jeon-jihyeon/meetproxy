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
	id := inbox.IdOf(link)
	allowed := step{"allow", []string{"slack:C1"}, ""}
	queued := step{"inbox", []string{"add", link, "--verdict", "handle"}, ""}
	prDelegation := `{"id":"pr","when":"mention","link":"github-pr","do":"review","post":"auto"}`
	builtIn := `{"id":"default-review","when":"mention","link":"github-pr","do":"review","post":"auto","approve":"self"},` +
		`{"id":"default-review-request","when":"review-request","do":"review","post":"ask","approve":"self"},` +
		`{"id":"default","when":"mention","do":"answer","post":"auto"}]`
	added := strconv.FormatInt(now.Unix(), 10)
	recorded := []step{
		{"open", []string{"o"}, ""},
		{"close", []string{"--topic", "할당 원인", "--keywords", "할당", "--paths", inRepo}, ""},
	}

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
		{"allow rejects a pattern without a normal form", "s1", false, nil, step{"allow", []string{"https://example.com"}, ""}, want{1, "", true}},
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
		{"route is gone since map plan places a request", "s1", false, recorded, step{"route", []string{"할당이"}, ""}, want{exitUsage, "", true}},
		{"usage error for open with two origins", "s1", false, nil, step{"open", []string{"o", "p"}, ""}, want{exitUsage, "", true}},
		{"usage error for close with an argument", "s1", false, []step{{"open", []string{"o"}, ""}}, step{"close", []string{"foo", "--topic", "t"}, ""}, want{exitUsage, "", true}},
		{"usage error for allowed with an argument", "s1", false, nil, step{"allowed", []string{"x"}, ""}, want{exitUsage, "", true}},
		{"usage error for a flag that does not parse", "s1", false, nil, step{"locate", []string{"x", "--limit", "many"}, ""}, want{exitUsage, "", true}},
		{"usage error for an unknown flag", "s1", false, nil, step{"locate", []string{"x", "--nope", "1"}, ""}, want{exitUsage, "", true}},
		{"usage error for a flag the command does not read", "s1", false, nil, step{"allowed", []string{"--topic", "t"}, ""}, want{exitUsage, "", true}},
		{"protocol needs no data directory", "s1", true, nil, step{"protocol", nil, ""}, want{0, strconv.Itoa(protocol), false}},
		{"usage error for tick without --cwd", "s1", false, nil, step{"tick", nil, ""}, want{exitUsage, "", true}},
		{
			"tick prints an empty list when nothing waits", "s1", false, nil, step{"tick", []string{"--cwd", "/tmp/notes"}, ""},
			want{0, `{"protocol":` + strconv.Itoa(protocol) + `,"place":"/tmp/notes","name":"notes","waiting":[]}`, false},
		},
		{"allowed lists the patterns", "s1", false, []step{allowed}, step{"allowed", nil, ""}, want{0, "slack:C1", false}},
		{
			"can-post allows where the open relay came from", "s1", false, []step{{"open", []string{"https://w.slack.com/archives/C7/p1"}, ""}},
			step{"can-post", []string{"https://w.slack.com/archives/C7/p2"}, ""}, want{0, "allowed", false},
		},
		{
			"can-post denies another place without an allow list entry", "s1", false, []step{{"open", []string{"https://w.slack.com/archives/C7/p1"}, ""}},
			step{"can-post", []string{"https://w.slack.com/archives/C8/p2"}, ""}, want{1, "denied", false},
		},
		{"can-post allows an allowed place with no relay", "s1", false, []step{allowed}, step{"can-post", []string{"slack:C1"}, ""}, want{0, "allowed", false}},
		{
			"inbox add keeps the place it is given and ignores the location map", "s1", false, append([]step{allowed}, recorded...),
			step{"inbox", []string{"add", link, "--verdict", "handle", "--keywords", "할당,지연", "--ts", "1800000000.0001", "--name", "web"}, ""},
			want{0, id + "\tnew\tweb\tnew", false},
		},
		{
			"inbox add keeps an ask request", "s1", false, []step{allowed},
			step{"inbox", []string{"add", link, "--verdict", "ask", "--reason", "deploy"}, ""}, want{0, id + "\task\t-\tnew", false},
		},
		{"inbox add reports a link seen before", "s1", false, []step{allowed, queued}, queued, want{0, id + "\tnew\t-\tseen", false}},
		{"inbox add takes a channel outside the allow list", "s1", false, nil, queued, want{0, id + "\tnew\t-\tnew", false}},
		{
			"usage error for inbox add without a verdict", "s1", false, []step{allowed},
			step{"inbox", []string{"add", link}, ""}, want{exitUsage, "", true},
		},
		{
			"inbox cursor moves with the newest request", "s1", false,
			[]step{allowed, {"inbox", []string{"add", link, "--verdict", "handle", "--ts", "1800000000.0001"}, ""}},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0001", false},
		},
		{
			"inbox add moves the cursor for a link seen before", "s1", false,
			[]step{
				{"inbox", []string{"add", link, "--verdict", "handle", "--ts", "1800000000.0001"}, ""},
				{"inbox", []string{"add", link, "--verdict", "handle", "--ts", "1800000000.0002"}, ""},
			},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0002", false},
		},
		{
			"inbox add asks again for a done request with a new timestamp", "s1", false,
			[]step{
				{"inbox", []string{"add", link, "--verdict", "handle", "--ts", "1800000000.0001"}, ""},
				{"inbox", []string{"done", id}, ""},
			},
			step{"inbox", []string{"add", link, "--verdict", "handle", "--ts", "1800000000.0002"}, ""}, want{0, id + "\tnew\t-\tnew", false},
		},
		{
			"inbox advance moves the cursor without a request", "s1", false,
			[]step{{"inbox", []string{"advance", "1800000000.0002"}, ""}},
			step{"inbox", []string{"cursor"}, ""}, want{0, "1800000000.0002", false},
		},
		{"inbox claim takes a request and prints its link", "s1", false, []step{allowed, queued}, step{"inbox", []string{"claim", id}, ""}, want{0, link, false}},
		{
			"inbox claim takes nothing while the session works on another request", "s1", false,
			[]step{allowed, queued, {"inbox", []string{"add", "https://w.slack.com/archives/C1/p2", "--verdict", "handle"}, ""},
				{"inbox", []string{"claim", inbox.IdOf("https://w.slack.com/archives/C1/p2")}, ""}},
			step{"inbox", []string{"claim", id}, ""}, want{0, "", false},
		},
		{
			"inbox waiting lists unclaimed requests with name and added time", "s1", false,
			[]step{allowed, queued, {"inbox", []string{"add", "https://w.slack.com/archives/C1/p2", "--verdict", "ask", "--name", "wiki"}, ""},
				{"inbox", []string{"take", id}, ""}},
			step{"inbox", []string{"waiting"}, ""},
			want{0, inbox.IdOf("https://w.slack.com/archives/C1/p2") + "\task\twiki\t" + added + "\thttps://w.slack.com/archives/C1/p2", false},
		},
		{
			"inbox done drops a request", "s1", false,
			[]step{allowed, queued, {"inbox", []string{"done", id}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\tdone\t-\t" + link, false},
		},
		{
			"inbox claim takes nothing while a relay is open", "s1", false,
			[]step{allowed, {"inbox", []string{"add", link, "--verdict", "handle"}, ""}, {"open", []string{"o"}, ""}},
			step{"inbox", []string{"claim", id}, ""}, want{0, "", false},
		},
		{
			"inbox take prints link, task, target and approve", "s1", false, []step{allowed, queued},
			step{"inbox", []string{"take", id}, ""}, want{0, link + "\tanswer\t-\tno\t-\t-\t-", false},
		},
		{
			"inbox take prints the place, skills and files of the work map", "s1", false,
			[]step{{"inbox", []string{"add", link, "--verdict", "handle", "--place", "/w/svc", "--skills", "incident-triage,review", "--files", "/w/svc/a.go"}, ""}},
			step{"inbox", []string{"take", id}, ""}, want{0, link + "\tanswer\t-\tno\t/w/svc\tincident-triage,review\t/w/svc/a.go", false},
		},
		{
			"inbox add keeps the task of a delegation and lets a review approve the user's request", "s1", false,
			[]step{allowed, {"delegation", []string{"put"}, prDelegation}, {"inbox", []string{
				"add", link, "--verdict", "handle", "--delegation", "pr",
				"--target", "https://github.com/o/r/pull/1", "--self", "yes", "--name", "r",
			}, ""}},
			step{"inbox", []string{"take", id}, ""}, want{0, link + "\treview\thttps://github.com/o/r/pull/1\tyes\t-\t-\t-", false},
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
			[]step{{"delegation", []string{"put"}, `{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com","do":"investigate","post":"auto"}`}},
			step{"delegation", nil, ""},
			want{0, `[{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com","do":"investigate","post":"auto"},` + builtIn, false},
		},
		{
			"delegation match picks a review for a pull request link", "s1", false, []step{{"delegation", []string{"put"}, prDelegation}},
			step{"delegation", []string{"match", "mention"}, `{"text":"<@U1> review https://github.com/o/svc/pull/7 please","from":"U2"}`},
			want{0, `{"delegation":"pr","task":"review","post":"auto","triage":false,"target":"https://github.com/o/svc/pull/7","workspace":"svc"}`, false},
		},
		{
			"delegation match reviews a requested pull request after asking", "s1", false, nil,
			step{"delegation", []string{"match", "review-request"}, `{"link":"https://github.com/o/svc/pull/9","text":"Review requested: fix","from":"a"}`},
			want{0, `{"delegation":"default-review-request","task":"review","post":"ask","triage":false,"target":"https://github.com/o/svc/pull/9","workspace":"svc"}`, false},
		},
		{
			"delegation match falls back to the default for a question", "s1", false, []step{{"delegation", []string{"put"}, prDelegation}},
			step{"delegation", []string{"match", "mention"}, `{"text":"where is alloc","from":"U2"}`},
			want{0, `{"delegation":"default","task":"answer","post":"auto","triage":true}`, false},
		},
		{
			"delegation match prints nothing when the author of a channel delegation differs", "s1", false,
			[]step{{"delegation", []string{"put"}, `{"id":"alerts","when":"channel","channel":"C1","host":"w.slack.com","from":["datadog"],"words":["Triggered"],"do":"investigate","post":"auto"}`}},
			step{"delegation", []string{"match", "alerts"}, `{"text":"Recovered: cpu","from":"U9","author":"Datadog"}`},
			want{0, "", false},
		},
		{
			"delegation remove drops a delegation", "s1", false,
			[]step{{"delegation", []string{"put"}, prDelegation}, {"delegation", []string{"remove", "pr"}, ""}},
			step{"delegation", nil, ""}, want{0, `[` + builtIn, false},
		},
		{
			"inbox ask hands a taken request back and closes its relay", "s1", false,
			[]step{allowed, queued, {"inbox", []string{"take", id}, ""}, {"open", []string{link}, ""},
				{"inbox", []string{"ask", id, "--reason", "needs a decision"}, ""}},
			step{"close", []string{"--topic", "t"}, ""}, want{1, "", true},
		},
		{
			"usage error for inbox take without a session", "", false, []step{allowed, queued},
			step{"inbox", []string{"take", id}, ""}, want{exitUsage, "", true},
		},
		{"usage error for inbox without a command", "s1", false, nil, step{"inbox", nil, ""}, want{exitUsage, "", true}},
		{"usage error for an unknown inbox command", "s1", false, nil, step{"inbox", []string{"drop"}, ""}, want{exitUsage, "", true}},
		{"inbox add fails while paused", "s1", false, []step{allowed, {"pause", nil, ""}}, queued, want{1, "", true}},
		{
			"inbox add works after resume", "s1", false, []step{allowed, {"pause", nil, ""}, {"resume", nil, ""}},
			queued, want{0, id + "\tnew\t-\tnew", false},
		},
		{
			"close marks the request it relayed done", "s1", false,
			[]step{allowed, queued, {"inbox", []string{"take", id}, ""},
				{"open", []string{link}, ""}, {"close", []string{"--topic", "t", "--paths", inRepo}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\tdone\t-\t" + link, false},
		},
		{"triage defaults to claude", "s1", false, nil, step{"triage", nil, ""}, want{0, "claude", false}},
		{
			"triage keeps a command line", "s1", false, []step{{"triage", []string{"command", "laya", "judge"}, ""}},
			step{"triage", nil, ""}, want{0, "command laya judge", false},
		},
		{"triage refuses an unknown engine", "s1", false, nil, step{"triage", []string{"gemini"}, ""}, want{1, "", true}},
		{"usage error for a second engine", "s1", false, nil, step{"triage", []string{"claude", "extra"}, ""}, want{exitUsage, "", true}},
		{"usage error for triage command without a command line", "s1", false, nil, step{"triage", []string{"command"}, ""}, want{exitUsage, "", true}},
		{"usage error for triage run on input that is no JSON", "s1", false, nil, step{"triage", []string{"run"}, "text"}, want{exitUsage, "", true}},
		{"usage error for triage prompt on input that is no JSON", "s1", false, nil, step{"triage", []string{"prompt"}, "text"}, want{exitUsage, "", true}},
		{
			"triage parse reads a verdict from a reply", "s1", false, nil,
			step{"triage", []string{"parse"}, "sure {\"verdict\":\"handle\",\"reason\":\"code question\"}"},
			want{0, `{"verdict":"handle","reason":"code question"}`, false},
		},
		{
			"triage run uses a command engine", "s1", false,
			[]step{{"triage", []string{"command", `echo '{"verdict":"ignore","reason":"fyi"}'`}, ""}},
			step{"triage", []string{"run"}, `{"text":"fyi"}`}, want{0, `{"verdict":"ignore","reason":"fyi"}`, false},
		},
		{
			"triage run asks when the engine fails", "s1", false, []step{{"triage", []string{"command", "exit 3"}, ""}},
			step{"triage", []string{"run"}, `{"text":"x"}`}, want{0, `{"verdict":"ask","reason":"triage failed: exit 3: exit status 3 "}`, false},
		},
		{
			"triage keeps a long flag inside a command line", "s1", false, []step{{"triage", []string{"command", "laya", "--limit", "5"}, ""}},
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
		{
			"inbox add fails on a link that names no place", "s1", false, nil,
			step{"inbox", []string{"add", "notalink", "--verdict", "handle"}, ""}, want{1, "", true},
		},
		{
			"inbox add fails on a timestamp the cursor refuses", "s1", false, nil,
			step{"inbox", []string{"add", link, "--verdict", "handle", "--ts", "abc"}, ""}, want{1, "", true},
		},
		{
			"inbox add fails on an unknown delegation", "s1", false, nil,
			step{"inbox", []string{"add", link, "--verdict", "handle", "--delegation", "nope"}, ""}, want{1, "", true},
		},
		{
			"inbox hold puts a taken request off", "s1", false,
			[]step{queued, {"inbox", []string{"take", id}, ""}, {"inbox", []string{"hold", id}, ""}},
			step{"inbox", []string{"list"}, ""}, want{0, id + "\theld\t-\t" + link, false},
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
		{"inbox", []string{"add", link, "--verdict", "handle"}, ""},
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
		{"ask after the relay failed to close", closedRelays, step{"inbox", []string{"ask", id, "--reason", "r"}, ""}, listed, id + "\task\t-\t" + link + "\tr"},
		{"hold after the relay failed to close", closedRelays, step{"inbox", []string{"hold", id}, ""}, listed, id + "\theld\t-\t" + link},
		{"done after the relay failed to close", closedRelays, step{"inbox", []string{"done", id}, ""}, listed, id + "\tdone\t-\t" + link},
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
	now := time.Now()
	repo := gitRepo(t, "svc", "a.go")
	web := filepath.Join(t.TempDir(), "web")
	older, newer := "https://w.slack.com/archives/C1/p1", "https://w.slack.com/archives/C1/p2"
	data := []string{"--data", t.TempDir()}
	adds := [][]string{
		{"add", older, "--verdict", "handle", "--name", "svc", "--place", repo},
		{"add", newer, "--verdict", "ask", "--name", "web"},
	}
	for i, a := range adds {
		code, err := run("inbox", slices.Concat(data, a), "s1", now.Add(time.Duration(i)*time.Second), nil, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, 0, code)
	}
	row := func(link, status, place, name string, added time.Time, here bool) string {
		return fmt.Sprintf(`{"id":%q,"status":%q,"place":%q,"name":%q,"added":%d,"link":%q,"here":%t}`,
			inbox.IdOf(link), status, place, name, added.Unix(), link, here)
	}

	tcs := []struct {
		name string
		cwd  string
		want string
	}{
		{
			"a request of the repository belongs here by its root", filepath.Join(repo, "a.go"),
			fmt.Sprintf(`{"protocol":%d,"place":%q,"name":"svc","waiting":[%s,%s]}`, protocol, repo,
				row(older, "new", repo, "svc", now, true), row(newer, "ask", "", "web", now.Add(time.Second), false)),
		},
		{
			"a request stored without a root belongs here by its name", web,
			fmt.Sprintf(`{"protocol":%d,"place":%q,"name":"web","waiting":[%s,%s]}`, protocol, web,
				row(older, "new", repo, "svc", now, false), row(newer, "ask", "", "web", now.Add(time.Second), true)),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			code, err := run("tick", slices.Concat(data, []string{"--cwd", tc.cwd}), "s1", now, nil, &out)
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
	line := func(link string) string { return inbox.IdOf(link) + "\tnew\t-\t" + link + "\t" }
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
				code, err := run("inbox", slices.Concat(data, []string{"add", link, "--verdict", "handle"}), "s1", at, nil, &bytes.Buffer{})
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
