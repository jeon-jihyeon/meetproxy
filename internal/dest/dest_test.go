package dest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

func TestLocation(t *testing.T) {
	t.Parallel()
	type want struct {
		loc string
		ok  bool
	}
	tcs := []struct {
		name string
		raw  string
		want want
	}{
		{"PR link", "https://github.com/o/r/pull/1#discussion_r9", want{"github:o/r", true}},
		{"Slack message link", "https://w.slack.com/archives/C01AB/p123?thread_ts=1.2", want{"slack:C01AB", true}},
		{"normal form stays", "slack:C01AB", want{"slack:C01AB", true}},
		{"unknown shape", "https://example.com/x", want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			loc, ok := dest.Location(tc.raw)
			assert.Equal(t, tc.want, want{loc, ok})
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
		{"allows a channel", args{[]string{"slack:C1"}, "https://w.slack.com/archives/C1/p123"}, true},
		{"denies another channel", args{[]string{"slack:C1"}, "https://w.slack.com/archives/C2/p123"}, false},
		{"denies input without a normal form", args{[]string{"*"}, "https://example.com/x"}, false},
		{"stores a duplicate once", args{[]string{"slack:C1", "slack:C1"}, "slack:C1"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := dest.New(t.TempDir())
			for _, p := range tc.args.patterns {
				require.NoError(t, a.Add(p))
			}
			ok, err := a.Allowed(tc.args.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestAllowAllowed_CorruptConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dest.json"), []byte("{"), 0o644))

	_, err := dest.New(dir).Allowed("slack:C1")

	assert.Error(t, err)
}
