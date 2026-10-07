package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

func mustRun(t *testing.T, data, session string, now time.Time, s step) string {
	t.Helper()
	var out bytes.Buffer
	code, err := run(s.cmd, append([]string{"--data", data}, s.args...), session, now, strings.NewReader(s.stdin), &out)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	return out.String()
}

func TestRun_Lease(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type hold struct {
		session string
		args    []string
		after   time.Duration
	}
	tcs := []struct {
		name  string
		holds []hold
		want  []int
	}{
		{"the first session holds it and another is refused", []hold{{"s1", []string{"hold", "slack"}, 0}, {"s2", []string{"hold", "slack"}, time.Minute}}, []int{0, 1}},
		{"a lease past its ttl goes to another session", []hold{{"s1", []string{"hold", "slack", "--ttl", "1m"}, 0}, {"s2", []string{"hold", "slack"}, 2 * time.Minute}}, []int{0, 0}},
		{"a dropped lease goes at once", []hold{{"s1", []string{"hold", "github"}, 0}, {"s1", []string{"drop", "github"}, 0}, {"s2", []string{"hold", "github"}, 0}}, []int{0, 0, 0}},
		{"each source has its own lease", []hold{{"s1", []string{"hold", "slack"}, 0}, {"s2", []string{"hold", "github"}, 0}}, []int{0, 0}},
		{"usage error without a session", []hold{{"", []string{"hold", "slack"}, 0}}, []int{exitUsage}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			got := make([]int, 0, len(tc.holds))
			for _, h := range tc.holds {
				code, _ := run("lease", append([]string{"--data", data}, h.args...), h.session, now.Add(h.after), nil, &bytes.Buffer{})
				got = append(got, code)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// Markers of the sessions with a scope
func markers(t *testing.T, data string) []string {
	t.Helper()
	entries, err := os.ReadDir(scopeDir(data))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The marker is there whenever the session has a scope and leaves with the turn that ends it
func TestScopeMarkers(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C7/p1"
	id := inbox.IdOf(link)
	queued := step{"inbox", []string{"add", link, "--verdict", "handle", "--trusted", "yes"}, ""}
	stop := step{"hook stop", nil, `{"session_id":"s1"}`}
	tcs := []struct {
		name  string
		steps []step
		want  []string
	}{
		{"nothing handled leaves no marker", []step{queued}, nil},
		{"an open relay marks the session", []step{{"open", []string{link}, ""}}, []string{"s1"}},
		{"a take marks the session", []step{queued, {"inbox", []string{"take", id}, ""}}, []string{"s1"}},
		{"a claim marks the session", []step{queued, {"inbox", []string{"claim", id}, ""}}, []string{"s1"}},
		{"a close keeps the marker for the rest of the turn", []step{{"open", []string{link}, ""}, {"close", []string{"--topic", "t"}, ""}}, []string{"s1"}},
		{"the turn that ends the scope removes the marker", []step{{"open", []string{link}, ""}, {"close", []string{"--topic", "t"}, ""}, stop}, []string{}},
		{"a turn that ends with the relay open keeps it", []step{{"open", []string{link}, ""}, stop}, []string{"s1"}},
		{"a settle keeps it until the turn ends", []step{queued, {"inbox", []string{"take", id}, ""}, {"inbox", []string{"done", id}, ""}}, []string{"s1"}},
		{"a settled take loses it with the turn", []step{queued, {"inbox", []string{"take", id}, ""}, {"inbox", []string{"done", id}, ""}, stop}, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			now := time.Now()
			for _, s := range tc.steps {
				if s.cmd == "hook stop" {
					require.NoError(t, runHook(data, []string{"stop"}, now, strings.NewReader(s.stdin), &bytes.Buffer{}))
					continue
				}
				mustRun(t, data, "s1", now, s)
			}
			assert.Equal(t, tc.want, markers(t, data))
		})
	}
}

// A claim another session won leaves no marker that would stop the launcher skipping
func TestScopeMarkers_LostClaim(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Now()
	link := "https://w.slack.com/archives/C7/p1"
	mustRun(t, data, "s2", now, step{"inbox", []string{"add", link, "--verdict", "handle", "--trusted", "yes"}, ""})
	mustRun(t, data, "s2", now, step{"inbox", []string{"take", inbox.IdOf(link)}, ""})

	code, _ := run("inbox", []string{"--data", data, "claim", inbox.IdOf(link)}, "s1", now, nil, &bytes.Buffer{})

	assert.Equal(t, exitFailed, code)
	assert.Equal(t, []string{"s2"}, markers(t, data))
}

// State from before markers gets a marker for every session with a scope before the directory appears
func TestScopeMarkers_Migration(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Now()
	_, err := relay.New(data).Open("old", "https://w.slack.com/archives/C7/p1", "", now)
	require.NoError(t, err)
	_, _, err = inbox.New(data).Add(inbox.Item{Link: "https://w.slack.com/archives/C7/p2", Status: inbox.StatusNew}, now)
	require.NoError(t, err)
	_, err = inbox.New(data).Take(inbox.IdOf("https://w.slack.com/archives/C7/p2"), "taker", now)
	require.NoError(t, err)
	input := `{"session_id":"s9","tool_name":"Read","tool_input":{"file_path":"/nowhere"}}`

	require.NoError(t, runHook(data, []string{"path"}, now, strings.NewReader(input), &bytes.Buffer{}))

	got := markers(t, data)
	slices.Sort(got)
	assert.Equal(t, []string{"old", "taker"}, got)
}

func TestRun_StatusAndHealth(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Unix(1893456000, 0)
	mustRun(t, data, "s1", now, step{"health", []string{"github"}, `{"ok":false,"error":"gh: HTTP 401","found":0}`})
	mustRun(t, data, "s1", now, step{"health", []string{"slack"}, `{"ok":true,"found":2}`})
	mustRun(t, data, "s1", now, step{"inbox", []string{"add", "https://w.slack.com/archives/C1/p1", "--verdict", "ask"}, ""})
	mustRun(t, data, "s1", now, step{"open", []string{"https://w.slack.com/archives/C1/p2"}, ""})
	require.NoError(t, os.WriteFile(filepath.Join(data, "inbox", inbox.IdOf("https://w.slack.com/archives/C1/p3")+".json"), []byte("{"), 0o600))
	// A hook that fails still exits 0 and leaves its failure for status
	require.Error(t, runHook(data, []string{"stop"}, now, strings.NewReader("{"), &bytes.Buffer{}))

	out := mustRun(t, data, "s1", now, step{"status", nil, ""})

	var st status
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	at := now.UTC()
	assert.Equal(t, map[string]sourceHealth{"github": {Error: "gh: HTTP 401", At: at}, "slack": {Ok: true, Found: 2, At: at}}, st.Sources)
	assert.Equal(t, inboxCounts{Waiting: 1, Corrupt: 1}, st.Inbox)
	assert.Equal(t, []any{protocol, 1, false, 1}, []any{st.Protocol, st.RelaysOpen, st.Slack.Token, len(st.Hooks)})
	assert.Equal(t, "stop", st.Hooks[0].Hook)
}

func TestRun_TidyDaily(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Now()
	old := now.Add(-15 * 24 * time.Hour)
	mustRun(t, data, "s1", old, step{"inbox", []string{"add", "https://w.slack.com/archives/C1/p1", "--verdict", "ask"}, ""})
	loose := filepath.Join(data, "dest.json")
	require.NoError(t, os.WriteFile(loose, []byte("{}"), 0o644))

	first := mustRun(t, data, "s1", now, step{"tidy", []string{"daily"}, ""})
	second := mustRun(t, data, "s1", now.Add(time.Hour), step{"tidy", []string{"daily"}, ""})

	rows := mustRun(t, data, "s1", now, step{"inbox", []string{"list"}, ""})
	info, err := os.Stat(loose)
	require.NoError(t, err)
	assert.Equal(t, []string{"tidied, 1 requests expired", ""}, []string{strings.TrimSpace(first), second})
	assert.Contains(t, rows, "\tdone\t")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, []string{}, markers(t, data))
}

// A tick during a turn keeps the session's take from aging into ask
func TestRun_TickBusy(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C1/p1"
	tcs := []struct {
		name string
		busy bool
		want int
	}{
		{"a busy tick renews the take", true, 0},
		{"an idle tick lets it age", false, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			start := time.Now()
			mustRun(t, data, "s1", start, step{"inbox", []string{"add", link, "--verdict", "handle", "--trusted", "yes"}, ""})
			mustRun(t, data, "s1", start, step{"inbox", []string{"take", inbox.IdOf(link)}, ""})
			args := []string{"--cwd", "/tmp/notes", "--session", "s1"}
			if tc.busy {
				args = append(args, "--busy")
			}
			mustRun(t, data, "", start.Add(15*time.Minute), step{"tick", args, ""})

			out := mustRun(t, data, "", start.Add(30*time.Minute), step{"tick", []string{"--cwd", "/tmp/notes"}, ""})

			var tk tick
			require.NoError(t, json.Unmarshal([]byte(out), &tk))
			assert.Len(t, tk.Waiting, tc.want)
		})
	}
}

// A busy tick after the take aged and the turn that ended took its marker never leaves a scope the launcher skips
func TestRun_TickBusy_Expired(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C1/p1"
	tcs := []struct {
		name  string
		after time.Duration
	}{
		{"a tick right after the take aged", 21 * time.Minute},
		{"a tick long after the take aged", 2 * time.Hour},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			start := time.Now()
			mustRun(t, data, "s1", start, step{"inbox", []string{"add", link, "--verdict", "handle", "--trusted", "yes"}, ""})
			mustRun(t, data, "s1", start, step{"inbox", []string{"take", inbox.IdOf(link)}, ""})
			require.NoError(t, runHook(data, []string{"stop"}, start.Add(tc.after), strings.NewReader(`{"session_id":"s1"}`), &bytes.Buffer{}))
			require.Equal(t, []string{}, markers(t, data))

			mustRun(t, data, "", start.Add(tc.after+time.Minute), step{"tick", []string{"--cwd", "/tmp/notes", "--session", "s1", "--busy"}, ""})

			_, scoped, err := scopeOf(data, "s1", start.Add(tc.after+2*time.Minute))
			require.NoError(t, err)
			assert.Equal(t, []any{false, []string{}}, []any{scoped, markers(t, data)})
		})
	}
}
