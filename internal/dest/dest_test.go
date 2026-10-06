package dest_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

func TestParse(t *testing.T) {
	t.Parallel()
	type want struct {
		loc dest.Location
		ok  bool
	}
	tcs := []struct {
		name string
		raw  string
		want want
	}{
		{"PR link", "https://github.com/o/r/pull/1#discussion_r9", want{dest.Location{Source: "github", Name: "o/r", Number: 1}, true}},
		{"issue link", "https://github.com/o/r/issues/12", want{dest.Location{Source: "github", Name: "o/r", Number: 12}, true}},
		{"repository link", "https://github.com/o/r/blob/main/a.go", want{dest.Location{Source: "github", Name: "o/r"}, true}},
		{"Slack message link", "https://w.slack.com/archives/C01AB/p123?thread_ts=1.2", want{dest.Location{Source: "slack", Name: "C01AB"}, true}},
		{"written form", "slack:C01AB", want{dest.Location{Source: "slack", Name: "C01AB"}, true}},
		{"written form with a number", "github:o/r#3", want{dest.Location{Source: "github", Name: "o/r", Number: 3}, true}},
		{"written form without a repository", "github:o", want{}},
		{"written form with a bad number", "github:o/r#x", want{}},
		{"written form with a pattern", "github:o/*", want{}},
		{"unknown shape", "https://example.com/x", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc, ok := dest.Parse(tc.raw)
			assert.Equal(t, tc.want, want{loc, ok})
		})
	}
}

func TestLocationCovers(t *testing.T) {
	t.Parallel()
	type args struct {
		at   string
		post string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a channel covers its messages", args{"slack:C1", "slack:C1"}, true},
		{"a pull request covers itself", args{"github:o/r#3", "github:o/r#3"}, true},
		{"a pull request does not cover another", args{"github:o/r#3", "github:o/r#4"}, false},
		{"a pull request does not cover its repository", args{"github:o/r#3", "github:o/r"}, false},
		{"a repository covers its pull requests", args{"github:o/r", "github:o/r#4"}, true},
		{"a repository does not cover another", args{"github:o/r", "github:o/x"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at, ok := dest.Parse(tc.args.at)
			require.True(t, ok)
			post, ok := dest.Parse(tc.args.post)
			require.True(t, ok)
			assert.Equal(t, tc.want, at.Covers(post))
		})
	}
}

func TestAllowAllowed(t *testing.T) {
	t.Parallel()
	type args struct {
		patterns []string
		raw      string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"denies without patterns", args{nil, "https://github.com/o/r/pull/1"}, false},
		{"allows an owner", args{[]string{"github:o/*"}, "https://github.com/o/r/pull/1"}, true},
		{"denies another owner", args{[]string{"github:o/*"}, "https://github.com/x/r/pull/1"}, false},
		{"allows every number of a repository", args{[]string{"github:o/r"}, "github:o/r#7"}, true},
		{"allows a channel", args{[]string{"slack:C1"}, "https://w.slack.com/archives/C1/p123"}, true},
		{"denies another channel", args{[]string{"slack:C1"}, "https://w.slack.com/archives/C2/p123"}, false},
		{"stores a duplicate once", args{[]string{"slack:C1", "slack:C1"}, "slack:C1"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := dest.New(t.TempDir())
			for _, p := range tc.args.patterns {
				require.NoError(t, a.Add(p))
			}
			loc, ok := dest.Parse(tc.args.raw)
			require.True(t, ok)
			got, err := a.Allowed(loc)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAllowAdd(t *testing.T) {
	t.Parallel()
	type want struct {
		err      bool
		patterns []string
	}
	tcs := []struct {
		name    string
		pattern string
		want    want
	}{
		{"takes an owner pattern", "github:o/*", want{false, []string{"github:o/*"}}},
		{"takes every repository", "github:*", want{false, []string{"github:*"}}},
		{"takes a channel", "slack:C1", want{false, []string{"slack:C1"}}},
		{"rejects an unknown source", "gitlab:o/r", want{true, nil}},
		{"rejects a link", "https://github.com/o/r", want{true, nil}},
		{"rejects a number", "github:o/r#3", want{true, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := dest.New(t.TempDir())
			err := a.Add(tc.pattern)
			ps, perr := a.Patterns()
			require.NoError(t, perr)
			assert.Equal(t, tc.want, want{err != nil, ps})
		})
	}
}

func TestAllowAdd_Concurrent(t *testing.T) {
	t.Parallel()
	a := dest.New(t.TempDir())
	want := []string{"slack:C1", "slack:C2", "slack:C3", "slack:C4", "slack:C5", "slack:C6", "slack:C7", "slack:C8"}

	var wg sync.WaitGroup
	for _, p := range want {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, a.Add(p))
		}()
	}
	wg.Wait()

	got, err := a.Patterns()
	require.NoError(t, err)
	assert.ElementsMatch(t, want, got)
}

func TestAllowAllowed_CorruptConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dest.json"), []byte("{"), 0o644))

	_, err := dest.New(dir).Allowed(dest.Location{Source: "slack", Name: "C1"})

	assert.Error(t, err)
}
