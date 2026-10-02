package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestRun(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "svc", "pkg/alloc.go")
	inRepo := filepath.Join(repo, "pkg", "alloc.go")

	type step struct {
		cmd  string
		args []string
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
			"close fails on evidence outside a repository", "s1", false,
			[]step{{"open", []string{"o"}}},
			step{"close", []string{"--topic", "t", "--paths", "/nowhere/x.go"}},
			want{1, "", true},
		},
		{
			"locate after close with flags after arguments", "s1", false,
			[]step{
				{"open", []string{"o"}},
				{"close", []string{"--topic", "할당 원인", "--keywords", "할당", "--paths", inRepo}},
			},
			step{"locate", []string{"할당이"}},
			want{0, "1\tsvc\t" + inRepo + "\t할당 원인", false},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := []string{"--data", t.TempDir()}
			now := time.Now()
			for _, s := range tc.setup {
				code, err := run(s.cmd, append(s.args, data...), tc.session, now, &bytes.Buffer{})
				require.NoError(t, err)
				require.Equal(t, 0, code)
			}
			runArgs := tc.run.args
			if !tc.noData {
				runArgs = append(runArgs, data...)
			}
			var out bytes.Buffer
			code, err := run(tc.run.cmd, runArgs, tc.session, now, &out)
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(out.String()), err != nil})
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
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, reorder(tc.args))
		})
	}
}
