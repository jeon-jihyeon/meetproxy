package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/workmap"
)

// Points the config at a transcript with one request typed in a repository and returns that repository
// Sets the environment so the test cannot run in parallel with others
func mapConfig(t *testing.T) string {
	t.Helper()
	repo := gitRepo(t, "svc", "alloc.go")
	config := t.TempDir()
	b, err := json.Marshal(map[string]any{"type": "user", "cwd": repo, "message": map[string]any{"content": "svc 할당 원인"}})
	require.NoError(t, err)
	dir := filepath.Join(config, "projects", "-svc")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.jsonl"), append(b, '\n'), 0o644))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	return repo
}

func TestMap(t *testing.T) {
	repo := mapConfig(t)
	now := time.Now()
	refreshed := step{"map", []string{"refresh"}, ""}
	shown := "1\tsvc\t" + repo + "\t[]\n0 skills and commands"
	for _, k := range workmap.Kinds {
		shown += "\n" + string(k) + "\t0 guides\t0 skills\t0 examples"
	}

	type want struct {
		code   int
		out    string
		failed bool
	}
	tcs := []struct {
		name  string
		setup []step
		run   step
		want  want
	}{
		{"usage error without a map command", nil, step{"map", nil, ""}, want{exitUsage, "", true}},
		{"usage error for an unknown map command", nil, step{"map", []string{"nope"}, ""}, want{exitUsage, "", true}},
		{"usage error for a refresh argument other than daily", nil, step{"map", []string{"refresh", "weekly"}, ""}, want{exitUsage, "", true}},
		{"usage error for a second refresh argument", nil, step{"map", []string{"refresh", "daily", "x"}, ""}, want{exitUsage, "", true}},
		{"refresh reads the transcripts", nil, refreshed, want{0, "refreshed", false}},
		{
			"a daily refresh after a refresh the same day does nothing", []step{refreshed},
			step{"map", []string{"refresh", "daily"}, ""}, want{0, "", false},
		},
		{"show prints the places, the count of methods and the formats", []step{refreshed}, step{"map", []string{"show"}, ""}, want{0, shown, false}},
		{"show before any refresh knows nothing", nil, step{"map", []string{"show"}, ""}, want{0, "0 skills and commands", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			for _, s := range tc.setup {
				code, err := run(s.cmd, append(s.args, data...), "s1", now, strings.NewReader(s.stdin), &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			var out bytes.Buffer
			code, err := run(tc.run.cmd, append(tc.run.args, data...), "s1", now, strings.NewReader(tc.run.stdin), &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
		})
	}
}

// A refresh another session runs already covers this one
func TestMapRefreshBusy(t *testing.T) {
	mapConfig(t)
	data := t.TempDir()
	lock := filepath.Join(data, "workmap", "refresh.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o755))
	f, err := os.Create(lock)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))

	var out bytes.Buffer
	code, err := run("map", []string{"refresh", "--data", data}, "s1", time.Now(), strings.NewReader(""), &out)
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Empty(t, out.String())
}

func TestMapFormat(t *testing.T) {
	repo := gitRepo(t, "svc", "alloc.go")
	config := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(config, "CLAUDE.md"), []byte("# Intro\n\n# Commit Messages\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Commits\n"), 0o644))
	lines := []map[string]any{
		{"type": "user", "cwd": repo, "message": map[string]any{"content": "svc 할당 원인"}},
		{"type": "assistant", "cwd": repo, "timestamp": "2030-01-01T00:00:00Z", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "u1", "name": "Bash", "input": map[string]any{"command": "git commit -m 'feat(svc): one'"}},
		}}},
		{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "u1"}}}},
	}
	var b []byte
	for _, l := range lines {
		j, err := json.Marshal(l)
		require.NoError(t, err)
		b = append(append(b, j...), '\n')
	}
	dir := filepath.Join(config, "projects", "-svc")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.jsonl"), b, 0o644))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	data := t.TempDir()
	code, err := run("map", []string{"refresh", "--data", data}, "s1", time.Now(), strings.NewReader(""), &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, 0, code)

	type want struct {
		code   int
		out    string
		failed bool
	}
	user := filepath.Join(config, "CLAUDE.md") + ":3 Commit Messages"
	example := "examples:\n--- example 1 of 1, 2030-01-01T00:00:00Z, " + repo + "\nfeat(svc): one"
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"guides then examples", []string{"commit"}, want{0, "guides:\n" + user + "\n" + filepath.Join(repo, "CLAUDE.md") + ":1 Commits\n\n" + example, false}},
		{"only the guides of the place", []string{"commit", "--place", "/elsewhere"}, want{0, "guides:\n" + user + "\n\n" + example, false}},
		{"nothing for a kind the map knows nothing of", []string{"sheet"}, want{0, "", false}},
		{"usage error for an unknown kind", []string{"tweet"}, want{exitUsage, "", true}},
		{"usage error without a kind", nil, want{exitUsage, "", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			code, err := run("map", append(append([]string{"format"}, tc.args...), "--data", data), "s1", time.Now(), strings.NewReader(""), &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
		})
	}
}
