package messageurl_test

import (
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/messageurl"
	"github.com/stretchr/testify/assert"
)

func TestParseLink(t *testing.T) {
	t.Parallel()
	type want struct {
		link messageurl.Link
		ok   bool
	}
	tcs := []struct {
		name string
		raw  string
		want want
	}{
		{"issue", "https://github.com/o/svc/issues/7", want{messageurl.Link{Source: "github", Repo: "o/svc", Number: 7}, true}},
		{"review comment", "https://github.com/o/svc/pull/7#discussion_r51", want{messageurl.Link{Source: "github", Repo: "o/svc", Number: 7, Pull: true, Discussion: "51"}, true}},
		{"issue comment", "https://github.com/o/svc/issues/7#issuecomment-51", want{messageurl.Link{Source: "github", Repo: "o/svc", Number: 7, Comment: "51"}, true}},
		{"review", "https://github.com/o/svc/pull/7#pullrequestreview-51", want{messageurl.Link{Source: "github", Repo: "o/svc", Number: 7, Pull: true, Review: "51"}, true}},
		{"Slack message", "https://acme.slack.com/archives/C1/p1893456100000001", want{messageurl.Link{Source: "slack", Channel: "C1", Ts: "1893456100.000001", ThreadTs: "1893456100.000001"}, true}},
		{"Slack reply", "https://acme.slack.com/archives/C1/p1893456100000001?thread_ts=1893456000.000001", want{messageurl.Link{Source: "slack", Channel: "C1", Ts: "1893456100.000001", ThreadTs: "1893456000.000001"}, true}},
		{"zero issue", "https://github.com/o/svc/issues/0", want{}},
		{"negative issue", "https://github.com/o/svc/issues/-1", want{}},
		{"overflow issue", "https://github.com/o/svc/issues/999999999999999999999999", want{}},
		{"other path", "https://github.com/o/svc/tree/7", want{}},
		{"other scheme", "http://github.com/o/svc/issues/7", want{}},
		{"other host", "https://example.com/o/svc/issues/7", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			link, ok := messageurl.ParseLink(tc.raw)
			assert.Equal(t, tc.want.link, link)
			assert.Equal(t, tc.want.ok, ok)
		})
	}
}
