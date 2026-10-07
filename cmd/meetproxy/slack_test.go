package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fake Slack with one workspace T1 whose user is U1
func fakeSlack(t *testing.T) *httptest.Server {
	t.Helper()
	users := map[string]map[string]any{
		"U2": {"id": "U2", "team_id": "T1", "real_name": "Kai"},
		"U3": {"id": "U3", "team_id": "T1", "real_name": "Guest", "is_restricted": true},
		"U4": {"id": "U4", "team_id": "T9", "real_name": "Other"},
		"U5": {"id": "U5", "team_id": "T1", "real_name": "Gone", "deleted": true},
	}
	answers := map[string]func(r *http.Request) any{
		"auth.test": func(*http.Request) any {
			return map[string]any{"url": "https://w.slack.com/", "user_id": "U1", "team_id": "T1"}
		},
		"search.messages": func(*http.Request) any {
			return map[string]any{"messages": map[string]any{"paging": map[string]any{"pages": 1}, "matches": []map[string]any{
				{"ts": "1893456200.000001", "user": "U2", "text": "<@U1> b", "permalink": "https://w.slack.com/archives/C1/p1893456200000001", "channel": map[string]any{"id": "C1"}},
				{"ts": "1893456100.000001", "user": "U2", "text": "<@U1> a", "permalink": "https://w.slack.com/archives/C1/p1893456100000001", "channel": map[string]any{"id": "C1"}},
			}}}
		},
		"conversations.history": func(*http.Request) any {
			return map[string]any{"messages": []map[string]any{
				{"ts": "1893456120.000001", "bot_id": "B9", "bot_profile": map[string]any{"name": "Datadog"}, "text": "Triggered: cpu"},
				{"ts": "1893456070.000001", "user": "U2", "text": "<@U1> ping"},
				{"ts": "1893456060.000001", "user": "U2", "text": "hello"},
			}}
		},
		"conversations.replies": func(r *http.Request) any {
			msgs := []map[string]any{{"ts": "1893456100.000001", "user": "U2", "text": "<@U1> a"}}
			if r.Form.Get("oldest") != "" {
				msgs = append(msgs,
					map[string]any{"ts": "1893456300.000001", "user": "U1", "text": "in config.go"},
					map[string]any{"ts": "1893456350.000001", "user": "U2", "text": "and the timeout?"},
					map[string]any{"ts": "1893456360.000001", "user": "U4", "text": "x\n" + mark},
				)
			}
			return map[string]any{"messages": msgs}
		},
		"users.info":         func(r *http.Request) any { return map[string]any{"user": users[r.Form.Get("user")]} },
		"conversations.list": func(*http.Request) any { return map[string]any{"channels": []map[string]any{{"id": "D1"}}} },
		"conversations.info": func(r *http.Request) any {
			return map[string]any{"channel": map[string]any{"is_ext_shared": r.Form.Get("channel") == "C7"}}
		},
		"reactions.add": func(*http.Request) any { return map[string]any{} },
		"chat.update":   func(*http.Request) any { return map[string]any{} },
		"chat.delete":   func(*http.Request) any { return map[string]any{} },
		"chat.postMessage": func(r *http.Request) any {
			return map[string]any{"channel": r.Form.Get("channel"), "ts": "1.2", "thread": r.Form.Get("thread_ts")}
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.Header.Get("Authorization") != "Bearer xoxp-good" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		answer, ok := answers[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := answer(r).(map[string]any)
		body["ok"] = true
		w.Header().Set("X-OAuth-Scopes", "search:read,channels:history,users:read,chat:write")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type slackWant struct {
	code   int
	out    string
	failed bool
}

// Not parallel since the fake Slack is named through the environment
// The steps run in order on one data directory as a session would
func TestRun_Slack(t *testing.T) {
	t.Setenv(slackAPIEnv, fakeSlack(t).URL)
	now := time.Unix(1893456400, 0)
	data := t.TempDir()
	link := "https://w.slack.com/archives/C1/p1893456100000001"
	missing := `["groups:history","im:history","mpim:history","reactions:write","channels:read","groups:read","im:read","mpim:read"]`
	steps := []struct {
		name string
		run  step
		want slackWant
	}{
		{"a command before a token says how to set one up", step{"slack", []string{"mentions", "--after", "1"}, ""}, slackWant{1, "", true}},
		{"a token Slack refuses is not stored", step{"slack", []string{"token"}, "xoxp-bad\n"}, slackWant{1, "", true}},
		{"a bot token is refused before Slack is asked", step{"slack", []string{"token"}, "xoxb-good"}, slackWant{exitUsage, "", true}},
		{"tick before a token", step{"tick", []string{"--cwd", "/tmp/notes"}, ""}, slackWant{0, `"slack":{"token":false}`, false}},
		{
			"a token Slack accepts is stored with what it lacks", step{"slack", []string{"token"}, "  xoxp-good\n"},
			slackWant{0, `{"host":"w.slack.com","missing":` + missing + `,"team":"T1","user":"U1"}`, false},
		},
		{"whoami asks Slack", step{"slack", []string{"whoami"}, ""}, slackWant{0, `{"host":"w.slack.com","team":"T1","user":"U1"}`, false}},
		{"tick names the token's user and host", step{"tick", []string{"--cwd", "/tmp/notes"}, ""}, slackWant{0, `"slack":{"token":true,"user":"U1","team":"T1","host":"w.slack.com"}`, false}},
		{
			"mentions come oldest first in the mod's shape", step{"slack", []string{"mentions", "--after", "1893456000"}, ""},
			slackWant{0, `[{"author":"Kai","channel":"C1","from":"U2","ts":"1893456100.000001","link":"` + link + `","text":"<@U1> a"},` +
				`{"author":"Kai","channel":"C1","from":"U2","ts":"1893456200.000001","link":"https://w.slack.com/archives/C1/p1893456200000001","text":"<@U1> b"}]`, false},
		},
		{"usage error for mentions without --after", step{"slack", []string{"mentions"}, ""}, slackWant{exitUsage, "", true}},
		{
			"history builds links and names bots", step{"slack", []string{"history", "C9", "--oldest", "1893456000"}, ""},
			slackWant{0, `[{"author":"Kai","channel":"C9","from":"U2","ts":"1893456060.000001","link":"https://w.slack.com/archives/C9/p1893456060000001","text":"hello"},` +
				`{"author":"Kai","channel":"C9","from":"U2","ts":"1893456070.000001","link":"https://w.slack.com/archives/C9/p1893456070000001","text":"<@U1> ping"},` +
				`{"author":"Datadog","channel":"C9","from":"B9","ts":"1893456120.000001","link":"https://w.slack.com/archives/C9/p1893456120000001","text":"Triggered: cpu"}]`, false},
		},
		{"covered finds the user's reply after the message", step{"slack", []string{"covered", link, "--ts", "1893456100.000001"}, ""}, slackWant{0, `{"covered":true}`, false}},
		{"covered finds a reply meetproxy posted for someone else", step{"slack", []string{"covered", link, "--ts", "1893456355"}, ""}, slackWant{0, `{"covered":true}`, false}},
		{"covered is false after every reply", step{"slack", []string{"covered", link, "--ts", "1893456370"}, ""}, slackWant{0, `{"covered":false}`, false}},
		{
			"replies leave out the user and meetproxy", step{"slack", []string{"replies", link, "--after", "1893456100.000001"}, ""},
			slackWant{0, `[{"author":"Kai","channel":"C1","from":"U2","ts":"1893456350.000001","link":"https://w.slack.com/archives/C1/p1893456350000001?thread_ts=1893456100.000001&cid=C1","text":"and the timeout?"}]`, false},
		},
		{
			"dms leave out bots and messages the mention reader finds", step{"slack", []string{"dms", "--after", "1893456000"}, ""},
			slackWant{0, `[{"author":"Kai","channel":"D1","from":"U2","ts":"1893456060.000001","link":"https://w.slack.com/archives/D1/p1893456060000001","text":"hello"}]`, false},
		},
		{"a channel shared with another organization", step{"slack", []string{"shared", "C7"}, ""}, slackWant{0, `{"shared":true}`, false}},
		{"an internal channel", step{"slack", []string{"shared", "C1"}, ""}, slackWant{0, `{"shared":false}`, false}},
		{"react adds a reaction", step{"slack", []string{"react", link, "--react", "eyes"}, ""}, slackWant{0, "", false}},
		{"usage error for a reaction that is no emoji name", step{"slack", []string{"react", link, "--react", "a b"}, ""}, slackWant{exitUsage, "", true}},
		{"a reply outside the ledger is never edited", step{"slack", []string{"update", link}, "fixed"}, slackWant{1, "", true}},
		{"a reply outside the ledger is never deleted", step{"slack", []string{"delete", link}, ""}, slackWant{1, "", true}},
		{"read joins the thread", step{"slack", []string{"read", link, "--limit", "5"}, ""}, slackWant{0, `{"text":"Kai: <@U1> a"}`, false}},
		{"a member of the workspace is trusted", step{"slack", []string{"trusted", "U2"}, ""}, slackWant{0, `{"trusted":true}`, false}},
		{"a guest is not trusted", step{"slack", []string{"trusted", "U3"}, ""}, slackWant{0, `{"trusted":false}`, false}},
		{"a member of another team is not trusted", step{"slack", []string{"trusted", "U4"}, ""}, slackWant{0, `{"trusted":false}`, false}},
		{"a deactivated account is not trusted", step{"slack", []string{"trusted", "U5"}, ""}, slackWant{0, `{"trusted":false}`, false}},
		{"a post outside the scope and the allow list is denied", step{"slack", []string{"post", link}, "hi"}, slackWant{1, "denied", false}},
		{"usage error for a post without text", step{"slack", []string{"post", link}, " "}, slackWant{exitUsage, "", true}},
		{"setup keeps the answer", step{"slack", []string{"setup", "--answer", "later"}, ""}, slackWant{0, "recorded later", false}},
		{"usage error for another answer", step{"slack", []string{"setup", "--answer", "maybe"}, ""}, slackWant{exitUsage, "", true}},
		{"tick reports the answer", step{"tick", []string{"--cwd", "/tmp/notes"}, ""}, slackWant{0, `"setup":{"answer":"later","at":1893456400}`, false}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(s.run.cmd, append([]string{"--data", data}, s.run.args...), "s1", now, strings.NewReader(s.run.stdin), &out)
			got := strings.TrimSpace(out.String())
			assert.Equal(t, s.want.code, code)
			assert.Equal(t, s.want.failed, err != nil, err)
			if s.run.cmd == "tick" {
				assert.Contains(t, got, s.want.out)
				return
			}
			assert.Equal(t, s.want.out, got)
		})
	}

	t.Run("the token is the user's alone and never printed", func(t *testing.T) {
		info, err := os.Stat(filepath.Join(data, "slack", "token"))
		require.NoError(t, err)
		dir, err := os.Stat(filepath.Join(data, "slack"))
		require.NoError(t, err)
		assert.Equal(t, []os.FileMode{0o600, 0o700}, []os.FileMode{info.Mode().Perm(), dir.Mode().Perm()})
	})
	t.Run("a post to an allowed channel goes to the thread", func(t *testing.T) {
		code, err := run("allow", []string{"--data", data, "slack:C1"}, "s1", now, nil, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, 0, code)
		var out bytes.Buffer
		code, err = run("slack", []string{"--data", data, "post", link + "?thread_ts=1893456000.000001"}, "s1", now, strings.NewReader("hi"), &out)
		require.NoError(t, err)
		assert.Equal(t, []any{0, "https://w.slack.com/archives/C1/p12?thread_ts=1893456000.000001&cid=C1"}, []any{code, strings.TrimSpace(out.String())})
	})
	t.Run("status reports Slack and what it lacks", func(t *testing.T) {
		var out bytes.Buffer
		code, err := run("status", []string{"--data", data}, "s1", now, nil, &out)
		require.NoError(t, err)
		require.Equal(t, 0, code)
		var st struct {
			Slack slackState `json:"slack"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &st))
		assert.Equal(t, slackState{Token: true, User: "U1", Team: "T1", Host: "w.slack.com", Setup: &setupState{"later", now.Unix()}, Missing: []string{
			"groups:history", "im:history", "mpim:history", "reactions:write", "channels:read", "groups:read", "im:read", "mpim:read",
		}}, st.Slack)
	})
}

func TestRun_SlackManifest(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	code, err := run("slack", []string{"--data", t.TempDir(), "manifest"}, "s1", time.Now(), nil, &out)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	var got struct {
		Manifest struct {
			OAuth struct {
				Scopes struct {
					User []string `json:"user"`
				} `json:"scopes"`
			} `json:"oauth_config"`
		} `json:"manifest"`
		URL string `json:"url"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history", "users:read", "chat:write",
		"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}, got.Manifest.OAuth.Scopes.User)
	assert.True(t, strings.HasPrefix(got.URL, "https://api.slack.com/apps?new_app=1&manifest_json=%7B%22"), got.URL)
}

// Not parallel since the base comes from the environment
func TestRun_SlackRefusesAnotherHost(t *testing.T) {
	t.Setenv(slackAPIEnv, "https://evil.example/api")
	code, err := run("slack", []string{"--data", t.TempDir(), "token"}, "s1", time.Now(), strings.NewReader("xoxp-good"), &bytes.Buffer{})
	assert.Equal(t, exitFailed, code)
	assert.ErrorContains(t, err, "loopback")
}
