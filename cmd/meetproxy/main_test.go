package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	cmd  string
	args []string
}

func TestRun(t *testing.T) {
	t.Parallel()
	now := time.Now()
	repo := gitRepo(t, "svc", "pkg/alloc.go")
	inRepo := filepath.Join(repo, "pkg", "alloc.go")
	allowed := step{"allow", []string{"slack:C1"}}
	recorded := []step{
		{"open", []string{"o"}},
		{"close", []string{"--topic", "할당 원인", "--keywords", "할당", "--paths", inRepo}},
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
		{"usage error without --data", "s1", true, nil, step{"locate", []string{"x"}}, want{exitUsage, "", true}},
		{"usage error for open without a session", "", false, nil, step{"open", []string{"o"}}, want{exitUsage, "", true}},
		{"usage error for an unknown command", "s1", false, nil, step{"nope", nil}, want{exitUsage, "", true}},
		{"dest denies without an allow list", "s1", false, nil, step{"dest", []string{"slack:C1"}}, want{1, "denied", false}},
		{
			"dest allows a link after allow", "s1", false,
			[]step{{"allow", []string{"slack:C1"}}},
			step{"dest", []string{"https://w.slack.com/archives/C1/p1"}},
			want{0, "allowed", false},
		},
		{"allow rejects a pattern without a normal form", "s1", false, nil, step{"allow", []string{"https://example.com"}}, want{1, "", true}},
		{
			"usage error for close without --topic", "s1", false,
			[]step{{"open", []string{"o"}}}, step{"close", nil}, want{exitUsage, "", true},
		},
		{"close fails without an open relay", "s1", false, nil, step{"close", []string{"--topic", "t"}}, want{1, "", true}},
		{
			"close skips evidence outside a repository and still closes", "s1", false,
			[]step{{"open", []string{"o"}}, {"close", []string{"--topic", "t", "--paths", "/nowhere/x.go"}}},
			step{"close", []string{"--topic", "t"}},
			want{1, "", true},
		},
		{
			"locate finds what close recorded", "s1", false,
			recorded,
			step{"locate", []string{"할당이"}},
			want{0, "1\tsvc\t" + inRepo + "\t할당 원인", false},
		},
		{"usage error for open with two origins", "s1", false, nil, step{"open", []string{"o", "p"}}, want{exitUsage, "", true}},
		{"usage error for close with an argument", "s1", false, []step{{"open", []string{"o"}}}, step{"close", []string{"foo", "--topic", "t"}}, want{exitUsage, "", true}},
		{"usage error for allowed with an argument", "s1", false, nil, step{"allowed", []string{"x"}}, want{exitUsage, "", true}},
		{"usage error for a flag that does not parse", "s1", false, nil, step{"locate", []string{"x", "--limit", "many"}}, want{exitUsage, "", true}},
		{"usage error for an unknown flag", "s1", false, nil, step{"locate", []string{"x", "--nope", "1"}}, want{exitUsage, "", true}},
		{"usage error for a flag the command does not read", "s1", false, nil, step{"allowed", []string{"--topic", "t"}}, want{exitUsage, "", true}},
		{"allowed lists the patterns", "s1", false, []step{allowed}, step{"allowed", nil}, want{0, "slack:C1", false}},
		{
			"can-post allows where the open relay came from", "s1", false, []step{{"open", []string{"https://w.slack.com/archives/C7/p1"}}},
			step{"can-post", []string{"https://w.slack.com/archives/C7/p2"}}, want{0, "allowed", false},
		},
		{
			"can-post denies another place without an allow list entry", "s1", false, []step{{"open", []string{"https://w.slack.com/archives/C7/p1"}}},
			step{"can-post", []string{"https://w.slack.com/archives/C8/p2"}}, want{1, "denied", false},
		},
		{"can-post allows an allowed place with no relay", "s1", false, []step{allowed}, step{"can-post", []string{"slack:C1"}}, want{0, "allowed", false}},
		{
			"open stores the target a post may go to", "s1", false,
			[]step{{"open", []string{"o", "--target", "https://github.com/o/r/pull/1"}}},
			step{"can-post", []string{"https://github.com/o/r/pull/1"}}, want{0, "allowed", false},
		},
		{"can-post fails on a link that names no place", "s1", false, nil, step{"can-post", []string{"notalink"}}, want{1, "", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			for _, s := range tc.setup {
				code, err := run(s.cmd, slices.Concat(data, s.args), tc.session, now, &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			runArgs := tc.run.args
			if !tc.noData {
				runArgs = slices.Concat(data, runArgs)
			}
			var out bytes.Buffer
			code, err := run(tc.run.cmd, runArgs, tc.session, now, &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
		})
	}
}

func TestRun_BrokenState(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	file := filepath.Join(data, "relay", "open", "s1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("{"), 0o644))
	var out bytes.Buffer
	code, err := run("can-post", []string{"slack:C1", "--data", data}, "s1", time.Now(), &out)
	require.Error(t, err)
	assert.Equal(t, exitFailed, code)
	assert.Empty(t, out.String())
}

// A close that fails part way leaves state the same close finishes when it runs again
func TestRun_Retry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	link := "https://w.slack.com/archives/C1/p1"
	evidence := gitRepo(t, "svc", "a.go")
	closing := step{"close", []string{"--topic", "t", "--paths", evidence}}
	located := step{"locate", []string{"t"}}

	tcs := []struct {
		name string
		// Made a directory for the first run so writing or reading it fails
		blocked string
	}{
		{"close after the location map failed", "map.jsonl"},
		{"close after the relay failed to close", filepath.Join("relay", "closed.jsonl")},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			runStep := func(s step, out *bytes.Buffer) (int, error) {
				return run(s.cmd, slices.Concat(data, s.args), "s1", now, out)
			}
			code, err := runStep(step{"open", []string{link}}, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, 0, code)
			blocked := filepath.Join(data[1], tc.blocked)
			require.NoError(t, os.RemoveAll(blocked))
			require.NoError(t, os.Mkdir(blocked, 0o755))
			code, err = runStep(closing, &bytes.Buffer{})
			require.Error(t, err)
			require.Equal(t, exitFailed, code)

			require.NoError(t, os.Remove(blocked))
			code, err = runStep(closing, &bytes.Buffer{})
			require.NoError(t, err)
			require.Equal(t, 0, code)
			var out bytes.Buffer
			_, err = runStep(located, &out)
			require.NoError(t, err)
			assert.Equal(t, "1\tsvc\t"+evidence+"\tt", strings.TrimSpace(out.String()))
			_, err = relay.New(data[1]).Current("s1")
			assert.ErrorIs(t, err, relay.ErrNoOpen)
		})
	}
}

func TestReorder(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		args []string
		want []string
	}{
		{"moves a double dash flag", []string{"x", "--data", "d"}, []string{"--data", "d", "x"}},
		{"moves a single dash flag", []string{"x", "-data", "d"}, []string{"-data", "d", "x"}},
		{"keeps an equals flag whole", []string{"--data=d", "x"}, []string{"--data=d", "x"}},
		{"keeps a lone dash as an argument", []string{"-", "x"}, []string{"-", "x"}},
		{
			"keeps every word after a double dash", []string{"x", "--data", "d", "--", "-p", "--limit", "5"},
			[]string{"--data", "d", "--", "x", "-p", "--limit", "5"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args))
		})
	}
}
