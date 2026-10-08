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
	queued := step{"inbox", []string{"add", link}, ""}
	stop := step{"hook stop", nil, `{"session_id":"s1"}`}
	end := step{"hook end", nil, `{"session_id":"s1"}`}
	tcs := []struct {
		name  string
		steps []step
		want  []string
	}{
		{"nothing handled leaves no marker", []step{queued}, nil},
		{"an open relay marks the session", []step{{"open", []string{link}, ""}}, []string{"s1"}},
		{"a take marks the session", []step{queued, {"inbox", []string{"take", id}, ""}}, []string{"s1"}},
		{
			"a close keeps the marker for the rest of the turn",
			[]step{{"open", []string{link}, ""}, {"close", nil, ""}}, []string{"s1"},
		},
		{
			"the turn that ends the scope removes the marker",
			[]step{{"open", []string{link}, ""}, {"close", nil, ""}, stop}, []string{},
		},
		{"a turn that ends with the relay open keeps it", []step{{"open", []string{link}, ""}, stop}, []string{"s1"}},
		{"a settle keeps it until the turn ends", []step{queued, {"inbox", []string{"take", id}, ""}, {"inbox", []string{"done", id}, ""}}, []string{"s1"}},
		{"a settled take loses it with the turn", []step{queued, {"inbox", []string{"take", id}, ""}, {"inbox", []string{"done", id}, ""}, stop}, []string{}},
		{"a take keeps it past the turn", []step{queued, {"inbox", []string{"take", id}, ""}, stop}, []string{"s1"}},
		{"the end of the session removes the marker of a take", []step{queued, {"inbox", []string{"take", id}, ""}, end}, []string{}},
		{"the end of the session removes the marker of an open relay", []step{{"open", []string{link}, ""}, end}, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			now := time.Now()
			for _, s := range tc.steps {
				if hook, ok := strings.CutPrefix(s.cmd, "hook "); ok {
					require.NoError(t, runHook(data, []string{hook}, now, strings.NewReader(s.stdin), &bytes.Buffer{}))
					continue
				}
				mustRun(t, data, "s1", now, s)
			}
			assert.Equal(t, tc.want, markers(t, data))
		})
	}
}

// A take another session won leaves no marker that would stop the launcher skipping
func TestScopeMarkers_LostTake(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Now()
	link := "https://w.slack.com/archives/C7/p1"
	mustRun(t, data, "s2", now, step{"inbox", []string{"add", link}, ""})
	mustRun(t, data, "s2", now, step{"inbox", []string{"take", inbox.IdOf(link)}, ""})

	code, err := run("inbox", []string{"--data", data, "take", inbox.IdOf(link)}, "s1", now, nil, &bytes.Buffer{})

	assert.ErrorIs(t, err, inbox.ErrTaken)

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
	_, _, err = inbox.New(data).AddLimited(inbox.Item{Link: "https://w.slack.com/archives/C7/p2"}, inbox.Limits{}, now)
	require.NoError(t, err)
	_, err = inbox.New(data).Take(inbox.IdOf("https://w.slack.com/archives/C7/p2"), "taker", now)
	require.NoError(t, err)
	input := `{"session_id":"s9","tool_name":"Bash","tool_input":{"command":"gh pr comment 1 -R o/r -b hi"}}`

	require.NoError(t, runHook(data, []string{"guard"}, now, strings.NewReader(input), &bytes.Buffer{}))

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
	mustRun(t, data, "s1", now, step{"inbox", []string{"add", "https://w.slack.com/archives/C1/p1", "--summary", "hi"}, ""})
	mustRun(t, data, "s1", now, step{"inbox", []string{"add", "https://w.slack.com/archives/C1/p4"}, ""})
	mustRun(t, data, "s1", now, step{"inbox", []string{"hold", inbox.IdOf("https://w.slack.com/archives/C1/p4")}, ""})
	mustRun(t, data, "s1", now, step{"open", []string{"https://w.slack.com/archives/C1/p2"}, ""})
	require.NoError(t, os.WriteFile(filepath.Join(data, "inbox", inbox.IdOf("https://w.slack.com/archives/C1/p3")+".json"), []byte("{"), 0o600))
	// A hook that fails still exits 0 and leaves its failure for status
	require.Error(t, runHook(data, []string{"stop"}, now, strings.NewReader("{"), &bytes.Buffer{}))

	out := mustRun(t, data, "s1", now, step{"status", nil, ""})

	var st status
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	at := now.UTC()
	assert.Equal(t, map[string]sourceHealth{"github": {Error: "gh: HTTP 401", At: at}, "slack": {Ok: true, Found: 2, At: at}}, st.Sources)
	assert.Equal(t, inboxCounts{Open: 1, Held: 1, Corrupt: 1}, st.Inbox)
	assert.Equal(t, []heldRow{{Id: inbox.IdOf("https://w.slack.com/archives/C1/p4"), Link: "https://w.slack.com/archives/C1/p4"}}, st.Held)
	assert.Equal(t, []any{protocol, 1, false, 1}, []any{st.Protocol, st.RelaysOpen, st.Slack.Token, len(st.Hooks)})
	assert.Equal(t, "stop", st.Hooks[0].Hook)
}

func TestRun_TidyDaily(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Now()
	old := now.Add(-15 * 24 * time.Hour)
	mustRun(t, data, "s1", old, step{"inbox", []string{"add", "https://w.slack.com/archives/C1/p1"}, ""})
	loose := filepath.Join(data, "dest.json")
	require.NoError(t, os.WriteFile(loose, []byte("{}"), 0o644))

	first := mustRun(t, data, "s1", now, step{"tidy", []string{"daily"}, ""})
	second := mustRun(t, data, "s1", now.Add(time.Hour), step{"tidy", []string{"daily"}, ""})

	rows := mustRun(t, data, "s1", now, step{"inbox", []string{"list"}, ""})
	info, err := os.Stat(loose)
	require.NoError(t, err)
	assert.Equal(t, []string{"tidied, 1 requests expired", ""}, []string{strings.TrimSpace(first), second})
	assert.Empty(t, rows)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, []string{}, markers(t, data))
}

// A take keeps the scope with no heartbeat until it settles, the session ends or a day passes
func TestScope_Take(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C1/p1"
	id := inbox.IdOf(link)
	type args struct {
		// Steps after the take each run at its offset
		steps []step
		after time.Duration
	}
	type want struct {
		scoped  bool
		markers []string
	}
	stop := step{"hook stop", nil, `{"session_id":"s1"}`}
	end := step{"hook end", nil, `{"session_id":"s1"}`}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"lasts an hour without a tick", args{nil, time.Hour}, want{true, []string{"s1"}}},
		{
			"lasts after the heartbeat of the session stops", args{[]step{{"tick", nil, ""}}, time.Hour},
			want{true, []string{"s1"}},
		},
		{"lasts past turns that end", args{[]step{stop, stop}, 23 * time.Hour}, want{true, []string{"s1"}}},
		{"ends a day after the take", args{nil, 25 * time.Hour}, want{false, []string{"s1"}}},
		{"ends with the turn of a done", args{[]step{{"inbox", []string{"done", id}, ""}, stop}, time.Hour}, want{false, []string{}}},
		{"stays in the turn of a done", args{[]step{{"inbox", []string{"done", id}, ""}}, time.Hour}, want{true, []string{"s1"}}},
		{"ends with the session", args{[]step{end}, time.Hour}, want{false, []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			start := time.Now()
			mustRun(t, data, "s1", start, step{"inbox", []string{"add", link}, ""})
			mustRun(t, data, "s1", start, step{"inbox", []string{"take", id}, ""})
			for i, s := range tc.args.steps {
				at := start.Add(time.Duration(i+1) * time.Minute)
				if hook, ok := strings.CutPrefix(s.cmd, "hook "); ok {
					require.NoError(t, runHook(data, []string{hook}, at, strings.NewReader(s.stdin), &bytes.Buffer{}))
					continue
				}
				mustRun(t, data, "s1", at, s)
			}

			_, scoped, err := scopeOf(data, "s1", start.Add(tc.args.after))

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{scoped, markers(t, data)})
		})
	}
}

// Other sessions see a take of a session whose ticks stopped as open
func TestRun_TickHeartbeat(t *testing.T) {
	t.Parallel()
	link := "https://w.slack.com/archives/C1/p1"
	tcs := []struct {
		name  string
		ticks []time.Duration
		after time.Duration
		// Statuses of the waiting rows the other session reads
		want []inbox.Status
	}{
		{
			"a take of a session that ticks stays taken", []time.Duration{time.Minute, 2 * time.Minute}, 4 * time.Minute,
			[]inbox.Status{},
		},
		{
			"a take of a session whose ticks stopped opens again", []time.Duration{time.Minute}, 5 * time.Minute,
			[]inbox.Status{inbox.StatusOpen},
		},
		{"a take of a session that never ticked waits a day", nil, time.Hour, []inbox.Status{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			start := time.Now()
			mustRun(t, data, "s1", start, step{"inbox", []string{"add", link}, ""})
			mustRun(t, data, "s1", start, step{"inbox", []string{"take", inbox.IdOf(link)}, ""})
			for _, at := range tc.ticks {
				mustRun(t, data, "s1", start.Add(at), step{"tick", nil, ""})
			}

			out := mustRun(t, data, "s2", start.Add(tc.after), step{"tick", []string{"--session", "s2"}, ""})

			var got struct {
				Waiting []waitingRow `json:"waiting"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			statuses := []inbox.Status{}
			for _, row := range got.Waiting {
				statuses = append(statuses, row.Status)
			}
			assert.Equal(t, tc.want, statuses)
		})
	}
}

// A check that found the same as the last one leaves health.json alone for a while
func TestRun_HealthUnchanged(t *testing.T) {
	t.Parallel()
	ok := `{"ok":true,"found":2}`
	tcs := []struct {
		name   string
		second string
		after  time.Duration
		want   time.Duration
	}{
		{"the same result within ten minutes keeps the time of the first", ok, 5 * time.Minute, 0},
		{"the same result past ten minutes is written again", ok, 11 * time.Minute, 11 * time.Minute},
		{"another result is written at once", `{"ok":true,"found":3}`, time.Minute, time.Minute},
		{"an error is written at once", `{"ok":false,"error":"HTTP 500","found":2}`, time.Minute, time.Minute},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			start := time.Unix(1893456000, 0)
			mustRun(t, data, "s1", start, step{"health", []string{"slack"}, ok})

			mustRun(t, data, "s1", start.Add(tc.after), step{"health", []string{"slack"}, tc.second})

			assert.Equal(t, start.Add(tc.want).UTC(), readHealth(data).Sources["slack"].At)
		})
	}
}
