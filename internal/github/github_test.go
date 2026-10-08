package github_test

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

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
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

func notification(reason, kind, url string) github.Notification {
	var n github.Notification
	n.Id, n.Reason, n.UpdatedAt = "n1", reason, time.Unix(1893456300, 0)
	n.Subject.Title, n.Subject.URL, n.Subject.Type = "Fix it", url, kind
	n.Repository.FullName = "o/r"
	return n
}

const (
	pr     = "https://api.github.com/repos/o/r/pulls/3"
	prLink = "https://github.com/o/r/pull/3"
	isLink = "https://github.com/o/r/issues/3"
	review = prLink + "#discussion_r"
	note   = prLink + "#issuecomment-"
)

func TestClientMentions(t *testing.T) {
	t.Parallel()
	type want struct {
		threads []string
		links   []string
	}
	tcs := []struct {
		name    string
		answers map[string]string
		want    want
	}{
		{
			"comments after the cursor that name the user, a review reply under its root", map[string]string{
				"GET repos/o/r/issues/3/comments": list(
					comment(11, 0, "kai", "@Me look", 1893456100, note+"11"),
					comment(12, 0, "kai", "no name", 1893456100, note+"12"),
					comment(13, 0, "kai", "@me old", 1893455000, note+"13"),
					comment(14, 0, "kai", "@meow is another", 1893456100, note+"14"),
				),
				"GET repos/o/r/pulls/3/comments": list(comment(51, 50, "kai", "@me here", 1893456200, review+"51")),
			},
			want{[]string{"github:o/r#3", "github:o/r#3:50"}, []string{note + "11", review + "51"}},
		},
		{
			"the pull request itself when no comment names the user", map[string]string{
				"GET repos/o/r/issues/3/comments": list(),
				"GET repos/o/r/pulls/3/comments":  list(),
				"GET " + pr:                       one(comment(3, 0, "kai", "cc @me", 1893456100, prLink)),
			},
			want{[]string{"github:o/r#3"}, []string{prLink}},
		},
		{
			"nothing when the pull request is older than the cursor", map[string]string{
				"GET repos/o/r/issues/3/comments": list(),
				"GET repos/o/r/pulls/3/comments":  list(),
				"GET " + pr:                       one(comment(3, 0, "kai", "cc @me", 1893455000, prLink)),
			},
			want{[]string{}, []string{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(tc.answers)
			msgs, err := c.Mentions(notification("mention", "PullRequest", pr), "me", after)
			require.NoError(t, err)
			got := want{[]string{}, []string{}}
			for _, m := range msgs {
				assert.Equal(t, []any{"github", "mention", "o/r", 3, "MEMBER"}, []any{m.Source, m.Kind, m.Repo, m.Number, m.Association})
				got.threads, got.links = append(got.threads, m.Thread), append(got.links, m.Link)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientReviewRequest(t *testing.T) {
	t.Parallel()
	asked := func(login string, at int64) map[string]any {
		return map[string]any{"event": "review_requested", "created_at": iso(at), "requested_reviewer": map[string]any{"login": login}}
	}
	type want struct {
		ts   []string
		text string
	}
	tcs := []struct {
		name     string
		timeline string
		want     want
	}{
		{"the last request of the user", list(asked("me", 1893456100), asked("ann", 1893456250)), want{[]string{"1893456100"}, "Review requested: Fix it\nplease"}},
		{"a request of a team", list(map[string]any{"event": "review_requested", "created_at": iso(1893456150), "requested_team": map[string]any{}}), want{[]string{"1893456150"}, "Review requested: Fix it\nplease"}},
		{"the notification time without an event", list(), want{[]string{"1893456300"}, "Review requested: Fix it\nplease"}},
		{"nothing for a request before the cursor", list(asked("me", 1893455000)), want{[]string{}, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(map[string]string{
				"GET " + pr:                       one(comment(3, 0, "kai", "please", 1893450000, prLink)),
				"GET repos/o/r/issues/3/timeline": tc.timeline,
			})
			msgs, err := c.ReviewRequest(notification("review_requested", "PullRequest", pr), "me", after)
			require.NoError(t, err)
			got := want{ts: []string{}}
			for _, m := range msgs {
				got.ts, got.text = append(got.ts, m.Ts), m.Text
				assert.Equal(t, []any{"review-request", "kai", prLink, "github:o/r#3"}, []any{m.Kind, m.From, m.Link, m.Thread})
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientOwnPull(t *testing.T) {
	t.Parallel()
	bot := comment(16, 0, "ci[bot]", "green", 1893456100, note+"16")
	tcs := []struct {
		name   string
		author string
		want   []string
	}{
		{"comments of others that do not name the user", "me", []string{note + "11", review + "51"}},
		{"nothing on a pull request of someone else", "kai", []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(map[string]string{
				"GET " + pr: one(comment(3, 0, tc.author, "", 1893450000, prLink)),
				"GET repos/o/r/issues/3/comments": list(
					comment(11, 0, "kai", "looks off", 1893456100, note+"11"),
					comment(12, 0, "me", "mine", 1893456100, note+"12"),
					comment(13, 0, "kai", "old", 1893455000, note+"13"),
					comment(14, 0, "kai", "@me named", 1893456100, note+"14"),
					bot,
				),
				"GET repos/o/r/pulls/3/comments": list(comment(51, 50, "ann", "why", 1893456200, review+"51")),
			})
			msgs, err := c.OwnPull(notification("author", "PullRequest", pr), "me", after)
			require.NoError(t, err)
			got := []string{}
			for _, m := range msgs {
				got = append(got, m.Link)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientCovered(t *testing.T) {
	t.Parallel()
	threads := map[string]string{
		"GET repos/o/r/pulls/3/comments": list(
			comment(50, 0, "kai", "root", 1893456000, review+"50"),
			comment(51, 50, "kai", "@me why", 1893456100, review+"51"),
			comment(52, 50, "me", "because", 1893456200, review+"52"),
			comment(61, 60, "me", "other thread", 1893456300, review+"61"),
		),
		"GET repos/o/r/issues/3/comments": list(comment(12, 0, "ann", "done\n\n"+github.Mark, 1893456200, note+"12")),
		"GET repos/o/r/pulls/3/reviews":   list(map[string]any{"user": map[string]any{"login": "me"}, "submitted_at": iso(1893456400)}),
		"GET repos/o/r/issues/4/comments": list(),
	}
	type args struct {
		link string
		ts   int64
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a reply of the user in the review thread of a reply", args{review + "51", 1893456100}, true},
		{"a reply in another review thread does not count", args{review + "51", 1893456250}, false},
		{"a comment meetproxy posted for someone else", args{prLink, 1893456100}, true},
		{"a review the user submitted later", args{prLink, 1893456300}, true},
		{"nothing after every answer", args{prLink, 1893456500}, false},
		{"an issue has no reviews", args{"https://github.com/o/r/issues/4", 1893456100}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(threads)
			l, ok := dest.ParseLink(tc.args.link)
			require.True(t, ok)
			got, err := c.Covered(l, "me", time.Unix(tc.args.ts, 0))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestClientReplies(t *testing.T) {
	t.Parallel()
	type want struct {
		links   []string
		threads []string
	}
	tcs := []struct {
		name    string
		link    string
		answers map[string]string
		want    want
	}{
		{
			"a review thread reply link reads the thread of its root", review + "51", map[string]string{
				"GET repos/o/r/pulls/3/comments": list(
					comment(53, 50, "kai", "and then?", 1893456300, review+"53"),
					comment(54, 50, "me", "mine", 1893456300, review+"54"),
					comment(61, 60, "kai", "other thread", 1893456300, review+"61"),
				),
				"GET repos/o/r/pulls/comments/51": one(comment(51, 50, "kai", "why", 1893455000, review+"51")),
			},
			want{[]string{review + "53"}, []string{"github:o/r#3:50"}},
		},
		{
			"an issue counts its comments of others", isLink, map[string]string{
				"GET repos/o/r/issues/3/comments": list(
					comment(11, 0, "kai", "more", 1893456300, note+"11"),
					comment(12, 0, "ann", "x\n"+github.Mark, 1893456300, note+"12"),
					comment(13, 0, "dependabot[bot]", "bump", 1893456300, note+"13"),
				),
			},
			want{[]string{note + "11"}, []string{"github:o/r#3"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := client(tc.answers)
			l, ok := dest.ParseLink(tc.link)
			require.True(t, ok)
			msgs, err := c.Replies(l, "me", after)
			require.NoError(t, err)
			got := want{[]string{}, []string{}}
			for _, m := range msgs {
				got.links, got.threads = append(got.links, m.Link), append(got.threads, m.Thread)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

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
			l, ok := dest.ParseLink(tc.link)
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
		change func(github.Client, dest.Link) error
	}
	type want struct {
		err   bool
		calls int
	}
	edit := func(c github.Client, l dest.Link) error { return c.Edit(l, "fixed") }
	del := func(c github.Client, l dest.Link) error { return c.Delete(l) }
	react := func(content string) func(github.Client, dest.Link) error {
		return func(c github.Client, l dest.Link) error { return c.React(l, content) }
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
			l, ok := dest.ParseLink(tc.args.link)
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
			l, ok := dest.ParseLink(tc.args.link)
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
