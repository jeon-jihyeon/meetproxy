package github_test

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/github"
)

// gh answers by method and API path
// A paginated read wraps the answer in one page
type fakeGH struct {
	answers map[string]string
	mu      sync.Mutex
	calls   []string
	stdin   []string
}

func (f *fakeGH) run(args []string, stdin []byte) ([]byte, error) {
	method, path, slurp := "GET", "", false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-X":
			method = args[i+1]
			i++
		case "-f", "-q", "--input":
			i++
		case "--slurp":
			slurp = true
		case "--paginate":
		default:
			if path == "" {
				path = args[i]
			}
		}
	}
	key := method + " " + path
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	if stdin != nil {
		f.stdin = append(f.stdin, string(stdin))
	}
	f.mu.Unlock()
	out, ok := f.answers[key]
	if !ok {
		return nil, errors.New("gh: no answer for " + key)
	}
	if slurp {
		out = "[" + out + "]"
	}
	return []byte(out), nil
}

func client(answers map[string]string) (github.Client, *fakeGH) {
	f := &fakeGH{answers: answers}
	return github.New(f.run), f
}

var after = time.Unix(1893456000, 0)

func iso(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }

func comment(id, replyTo int64, login, body string, at int64, link string) map[string]any {
	return map[string]any{
		"id": id, "in_reply_to_id": replyTo, "user": map[string]any{"login": login, "type": "User"}, "body": body,
		"created_at": iso(at), "html_url": link, "author_association": "MEMBER",
	}
}

func list(items ...map[string]any) string {
	b, _ := json.Marshal(items)
	return string(b)
}

func one(item map[string]any) string {
	b, _ := json.Marshal(item)
	return string(b)
}

const (
	pr     = "https://api.github.com/repos/o/r/pulls/3"
	prLink = "https://github.com/o/r/pull/3"
	isLink = "https://github.com/o/r/issues/3"
	review = prLink + "#discussion_r"
	note   = prLink + "#issuecomment-"
)

func TestClientPost(t *testing.T) {
	t.Parallel()
	type want struct {
		link  string
		calls []string
	}
	tcs := []struct {
		name    string
		link    string
		answers map[string]string
		want    want
	}{
		{
			"a review reply goes to the root of its thread", review + "51", map[string]string{
				"GET repos/o/r/pulls/comments/51":            one(comment(51, 50, "kai", "why", 1, review+"51")),
				"POST repos/o/r/pulls/3/comments/50/replies": one(comment(55, 50, "me", "hi", 2, review+"55")),
			},
			want{review + "55", []string{"api repos/o/r/pulls/comments/51", "api -X POST repos/o/r/pulls/3/comments/50/replies --input -"}},
		},
		{
			"anything else is a comment on the issue", note + "11", map[string]string{
				"POST repos/o/r/issues/3/comments": one(comment(15, 0, "me", "hi", 2, note+"15")),
			},
			want{note + "15", []string{"api -X POST repos/o/r/issues/3/comments --input -"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, f := client(tc.answers)
			l, ok := messageurl.ParseLink(tc.link)
			require.True(t, ok)
			got, err := c.Post(l, "hi")
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{got, f.calls})
			assert.Equal(t, []string{`{"body":"hi"}`}, f.stdin)
		})
	}
}

func TestClientChange(t *testing.T) {
	t.Parallel()
	answers := map[string]string{
		"PATCH repos/o/r/issues/comments/11":         "{}",
		"DELETE repos/o/r/pulls/comments/51":         "",
		"POST repos/o/r/issues/3/reactions":          "{}",
		"POST repos/o/r/pulls/comments/51/reactions": "{}",
	}
	type args struct {
		link   string
		change func(github.Client, messageurl.Link) error
	}
	type want struct {
		err   bool
		calls int
	}
	edit := func(c github.Client, l messageurl.Link) error { return c.Edit(l, "fixed") }
	del := func(c github.Client, l messageurl.Link) error { return c.Delete(l) }
	react := func(content string) func(github.Client, messageurl.Link) error {
		return func(c github.Client, l messageurl.Link) error { return c.React(l, content) }
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"edits an issue comment", args{note + "11", edit}, want{false, 1}},
		{"deletes a review comment", args{review + "51", del}, want{false, 1}},
		{"refuses to edit the issue itself", args{isLink, edit}, want{true, 0}},
		{"refuses to delete the pull request itself", args{prLink, del}, want{true, 0}},
		{"reacts on the issue", args{isLink, react("eyes")}, want{false, 1}},
		{"reacts on a review comment itself", args{review + "51", react("hooray")}, want{false, 1}},
		{"refuses a reaction GitHub does not have", args{isLink, react("white_check_mark")}, want{true, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, f := client(answers)
			l, ok := messageurl.ParseLink(tc.args.link)
			require.True(t, ok)
			err := tc.args.change(c, l)
			assert.Equal(t, tc.want, want{err != nil, len(f.calls)}, err)
		})
	}
}

func TestClientRead(t *testing.T) {
	t.Parallel()
	answers := map[string]string{
		"GET repos/o/r/issues/3": one(map[string]any{"title": "Broken", "body": "it fails", "user": map[string]any{"login": "kai"}}),
		"GET repos/o/r/issues/3/comments": list(
			comment(11, 0, "ann", "same here", 1, note+"11"),
			comment(12, 0, "kai", "any news", 2, note+"12"),
		),
		"GET repos/o/r/issues/comments/11": one(comment(11, 0, "ann", "same here", 1, note+"11")),
		"GET repos/o/r/pulls/3/comments": list(
			comment(50, 0, "kai", "root", 1, review+"50"),
			comment(51, 50, "ann", "reply", 2, review+"51"),
			comment(60, 0, "kai", "other", 3, review+"60"),
		),
	}
	type args struct {
		link  string
		limit int
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"an issue with its first comments", args{isLink, 2}, "kai: Broken\nit fails\nann: same here"},
		{"one comment", args{note + "11", 5}, "ann: same here"},
		{"a review thread from its root", args{review + "51", 5}, "kai: root\nann: reply"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(answers)
			l, ok := messageurl.ParseLink(tc.args.link)
			require.True(t, ok)
			got, err := c.Read(l, tc.args.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientNotifications(t *testing.T) {
	t.Parallel()
	c, f := client(map[string]string{
		"GET notifications": `[{"id":"1","reason":"mention","updated_at":"2030-01-01T00:05:00Z","subject":{"url":"` + pr + `"}}]`,
	})
	got, err := c.Notifications(after)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []any{"1", "mention", pr}, []any{got[0].Key(), got[0].Reason, got[0].Subject.URL})
	assert.True(t, slices.ContainsFunc(f.calls, func(s string) bool {
		return strings.Contains(s, "-f participating=true -f since=2030-01-01T00:00:00Z")
	}), f.calls)
}
