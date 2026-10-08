package dest_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

// One case of the corpus the watcher checks its regexes against too
type linkCase struct {
	Name       string `json:"name"`
	Link       string `json:"link"`
	Source     string `json:"source"`
	Channel    string `json:"channel,omitempty"`
	Ts         string `json:"ts,omitempty"`
	ThreadTs   string `json:"thread_ts,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Number     int    `json:"number,omitempty"`
	Pull       bool   `json:"pull,omitempty"`
	Discussion string `json:"discussion,omitempty"`
	Comment    string `json:"comment,omitempty"`
	Thread     string `json:"thread,omitempty"`
}

func TestParseLink_Corpus(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../plugin/hooks/testdata/links.json")
	require.NoError(t, err)
	var tcs []linkCase
	require.NoError(t, json.Unmarshal(b, &tcs))
	require.NotEmpty(t, tcs)
	for _, tc := range tcs {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			l, ok := dest.ParseLink(tc.Link)
			got := linkCase{Name: tc.Name, Link: tc.Link}
			if ok {
				got = linkCase{
					Name: tc.Name, Link: tc.Link, Source: l.Source, Channel: l.Channel, Ts: l.Ts, ThreadTs: l.ThreadTs,
					Repo: l.Repo, Number: l.Number, Pull: l.Pull, Discussion: l.Discussion, Comment: l.Comment, Thread: l.Thread(),
				}
			}
			assert.Equal(t, tc, got)
		})
	}
}

func TestGitHubThread(t *testing.T) {
	t.Parallel()
	type args struct {
		repo   string
		number int
		root   string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"an issue or pull request", args{"o/r", 3, ""}, "github:o/r#3"},
		{"a review thread names its root comment", args{"o/r", 3, "9"}, "github:o/r#3:9"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, dest.GitHubThread(tc.args.repo, tc.args.number, tc.args.root))
		})
	}
}
