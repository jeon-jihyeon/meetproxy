package slackmcp_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/slackmcp"
)

func answer(t *testing.T, v any) slackmcp.Result {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return slackmcp.Result{Content: []slackmcp.Content{{Type: "text", Text: string(b)}}}
}

func searchResult(n int, from, ts, link, text string) string {
	return fmt.Sprintf("### Result %d\nChannel: #dev (ID: C1)\nFrom: P%d <p@example.com> (ID: %s) \nMessage_ts: %s\nPermalink: [link](%s)\nText: \n%s\n\n---\n", n, n, from, ts, link, text)
}

func TestNextPage(t *testing.T) {
	got, err := slackmcp.NextPage(answer(t, map[string]string{"pagination_info": "use cursor: `abc=`"}))
	require.NoError(t, err)
	assert.Equal(t, "abc=", got)
	got, err = slackmcp.NextPage(slackmcp.Result{})
	require.NoError(t, err)
	assert.Empty(t, got)
	_, err = slackmcp.NextPage(answer(t, map[string]string{"pagination_info": "more on the next page"}))
	assert.ErrorIs(t, err, slackmcp.ErrPagination)
	_, err = slackmcp.NextPage(answer(t, map[string]any{"has_more": true}))
	assert.ErrorIs(t, err, slackmcp.ErrMore)
}

func TestChannelMessages_KeepsBody(t *testing.T) {
	r := answer(t, map[string]string{"messages": "Channel: #ops (C9)\n\n=== Message from Kai (K), Team <kai@example.com> (U2) at 2030-01-01 12:10:15 UTC === \nMessage TS: 1893456100.000001\n" +
		"<!subteam^S1> review please\nPR: link\n\nmore context\nThread: 3 replies (latest: 2030-01-01 12:25:56 UTC)"})
	got, err := slackmcp.ChannelMessages(r, "C9", "acme.slack.com")
	require.NoError(t, err)
	assert.Equal(t, []slackmcp.Message{{
		Author: "Kai", From: "U2", Channel: "C9", Ts: "1893456100.000001", Link: "https://acme.slack.com/archives/C9/p1893456100000001",
		Text: "<!subteam^S1> review please\nPR: link\n\nmore context\nThread: 3 replies (latest: 2030-01-01 12:25:56 UTC)",
	}}, got)
	assert.Equal(t, "ops", slackmcp.ChannelTitle(r, "C9"))
	assert.Empty(t, slackmcp.ChannelTitle(r, "C8"))
}

// A display name that holds its own (ID) at never becomes the author's id
func TestChannelMessages_ForgedDisplayNameKeepsTheRealId(t *testing.T) {
	r := answer(t, map[string]string{"messages": "=== Message from Eve (U1) at x (U3) at 2030-01-01 12:10:15 UTC ===\nMessage TS: 1893456100.000001\nhi\n"})
	got, err := slackmcp.ChannelMessages(r, "C9", "acme.slack.com")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "U3", got[0].From)
}

func TestThreadMessages(t *testing.T) {
	r := answer(t, map[string]string{
		"messages": "=== THREAD PARENT MESSAGE ===\nFrom: Kai (K) <kai@example.com> (U2)\nTime: 2030-01-01 09:44:06 UTC\nMessage TS: 1893456100.000001\n" +
			"<@ME> where is retry\nReactions: eyes (1)\n\n=== THREAD REPLIES (1 total) ===\n\n--- Reply 1 of 1 ---\n" +
			"From: Ann <ann@example.com> (U3)\nTime: 2030-01-01 09:55:19 UTC\nMessage TS: 1893456200.000001\nin config.go\nline two\n",
		"pagination_info": "There are no more messages in this thread.\n",
	})
	got, err := slackmcp.ThreadMessages(r, "C7", "acme.slack.com")
	require.NoError(t, err)
	assert.Equal(t, []slackmcp.Message{
		{Author: "Kai", From: "U2", Channel: "C7", Ts: "1893456100.000001", Link: "https://acme.slack.com/archives/C7/p1893456100000001", Text: "<@ME> where is retry\nReactions: eyes (1)\n\n=== THREAD REPLIES (1 total) ==="},
		{Author: "Ann", From: "U3", Channel: "C7", Ts: "1893456200.000001", Link: "https://acme.slack.com/archives/C7/p1893456200000001", Text: "in config.go\nline two"},
	}, got)
	assert.Equal(t, []string{"eyes (1)"}, slackmcp.ReactionLines(got[0]))
	next, err := slackmcp.NextPage(r)
	require.NoError(t, err)
	assert.Empty(t, next)
}

func TestSearchMessages(t *testing.T) {
	r := answer(t, map[string]string{"results": searchResult(2, "U2", "1893456200.000001", "https://acme.slack.com/archives/C1/p1893456200000001", "second") +
		searchResult(1, "U1", "1893456100.000001", "https://acme.slack.com/archives/C1/p1893456100000001", "first")})
	got, err := slackmcp.SearchMessages(r)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, slackmcp.Message{Author: "P1", From: "U1", Channel: "C1", Ts: "1893456100.000001", Link: "https://acme.slack.com/archives/C1/p1893456100000001", Text: "first\n\n---"}, got[0])
}

func TestDelimiterInRequestText(t *testing.T) {
	for _, line := range []string{"Reactions: this line is part of the request", "Thread: 2 replies is part of this example",
		"--- Reply 1 of 1 ---", "=== Message from an example", "### Result 1 of 1"} {
		body := "please analyze\n" + line + "\nthen check retry"
		c, err := slackmcp.ChannelMessages(answer(t, map[string]string{"messages": "=== Message from Ann <ann@example.com> (U1) at x ===\nMessage TS: 1893456000.000001\n" + body}), "C1", "acme.slack.com")
		require.NoError(t, err, line)
		assert.Equal(t, body, c[0].Text, line)
		th, err := slackmcp.ThreadMessages(answer(t, map[string]string{"messages": "=== THREAD PARENT MESSAGE ===\nFrom: Ann <ann@example.com> (U1)\nMessage TS: 1893456000.000001\n" + body}), "C1", "acme.slack.com")
		require.NoError(t, err, line)
		assert.Equal(t, body, th[0].Text, line)
		s, err := slackmcp.SearchMessages(answer(t, map[string]string{"results": searchResult(1, "U1", "1893456000.000001", "https://acme.slack.com/archives/C1/p1893456000000001", body)}))
		require.NoError(t, err, line)
		assert.Contains(t, s[0].Text, body, line)
	}
}

func TestIncompleteHeader(t *testing.T) {
	cases := map[string]func(slackmcp.Result) ([]slackmcp.Message, error){
		"=== THREAD PARENT MESSAGE ===\nFrom: Ann (U1)\nMessage TS: 1893456000.000001\nrequest\n--- Reply 1 of 1 ---\nFrom: Kai (U2)\nmissing timestamp": func(r slackmcp.Result) ([]slackmcp.Message, error) {
			return slackmcp.ThreadMessages(r, "C1", "acme.slack.com")
		},
		"=== Message from Ann (U1) at x ===\nMessage TS: 1893456000.000001\nrequest\n=== Message from Kai (U2) at x ===\nMessage TS: unreadable\nrequest tail": func(r slackmcp.Result) ([]slackmcp.Message, error) {
			return slackmcp.ChannelMessages(r, "C1", "acme.slack.com")
		},
		"### Result 1 of 2\nChannel: #dev (ID: C1)\nFrom: Ann (ID: U1)\nMessage_ts: 1893456000.000001\nPermalink: [link](https://acme.slack.com/archives/C1/p1893456000000001)\nText: \nrequest\n### Result 2 of 2\nChannel: #dev (ID: C1)\nFrom: Kai (ID: U2)\nrequest tail": slackmcp.SearchMessages,
	}
	for page, parse := range cases {
		_, err := parse(answer(t, map[string]string{"messages": page, "results": page}))
		assert.ErrorIs(t, err, slackmcp.ErrHeader)
	}
}

func TestPrefix(t *testing.T) {
	_, err := slackmcp.ThreadMessages(answer(t, map[string]string{"messages": "unrecognized request prefix\n=== THREAD PARENT MESSAGE ===\nFrom: Ann (U1)\nMessage TS: 1893456000.000001\nrequest"}), "C1", "acme.slack.com")
	assert.ErrorIs(t, err, slackmcp.ErrPrefix)
}

func TestRecognized(t *testing.T) {
	_, err := slackmcp.Recognized(answer(t, map[string]string{"messages": "something new"}), "messages", nil)
	assert.ErrorIs(t, err, slackmcp.ErrFormat)
	got, err := slackmcp.Recognized(answer(t, map[string]string{"messages": "No messages found"}), "messages", nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestUserIdAndSentLink(t *testing.T) {
	r := answer(t, map[string]string{"result": "User ID: U9\nName: ann\n"})
	assert.Equal(t, "U9", slackmcp.UserId(r))
	sent := slackmcp.Result{Content: []slackmcp.Content{{Type: "text", Text: "Sent: https://acme.slack.com/archives/C1/p1893456100000002?thread_ts=1893456100.000001 done"}}}
	assert.Equal(t, "https://acme.slack.com/archives/C1/p1893456100000002?thread_ts=1893456100.000001", slackmcp.SentLinkIn(sent, "C1", "1893456100.000001", "done"))
	assert.Empty(t, slackmcp.SentLinkIn(slackmcp.Result{}, "C1", "", ""))
}

// A body that quotes a link of another channel never names the reply
func TestSentLinkIn_SkipsAnotherChannel(t *testing.T) {
	sent := slackmcp.Result{Content: []slackmcp.Content{{Type: "text", Text: "see https://acme.slack.com/archives/C9/p1700000000000100\nMessage sent: https://acme.slack.com/archives/C1/p1700000001000200"}}}
	assert.Equal(t, "https://acme.slack.com/archives/C1/p1700000001000200", slackmcp.SentLinkIn(sent, "C1", "", "see https://acme.slack.com/archives/C9/p1700000000000100"))
	assert.Empty(t, slackmcp.SentLinkIn(sent, "C2", "", ""))
}

// A link of the same channel the body quotes, the thread parent or another thread is never the reply
func TestSentLinkIn_SkipsWhatIsNotTheReply(t *testing.T) {
	origin := "https://acme.slack.com/archives/C1/p1700000000000100"
	reply := "https://acme.slack.com/archives/C1/p1700000001000200?thread_ts=1700000000.000100"
	tcs := []struct {
		name   string
		answer string
		thread string
		body   string
		want   string
	}{
		{"the body quotes the origin", "see " + origin + "\nMessage sent: " + reply, "1700000000.000100", "Fixed, see " + origin, reply},
		{"the body quotes another message of the channel", "see https://acme.slack.com/archives/C1/p1690000000000100\nMessage sent: https://acme.slack.com/archives/C1/p1700000001000200", "",
			"see https://acme.slack.com/archives/C1/p1690000000000100", "https://acme.slack.com/archives/C1/p1700000001000200"},
		{"the body quoted after the sent link", "Message sent: https://acme.slack.com/archives/C1/p1700000001000200\n> see https://acme.slack.com/archives/C1/p1700000009000100", "",
			"see https://acme.slack.com/archives/C1/p1700000009000100", "https://acme.slack.com/archives/C1/p1700000001000200"},
		{"the parent alone", "In reply to " + origin, "1700000000.000100", "ok", ""},
		{"a link in another thread", "https://acme.slack.com/archives/C1/p1700000001000200?thread_ts=1690000000.000100", "1700000000.000100", "ok", ""},
	}
	for _, tc := range tcs {
		r := slackmcp.Result{Content: []slackmcp.Content{{Type: "text", Text: tc.answer}}}
		assert.Equal(t, tc.want, slackmcp.SentLinkIn(r, "C1", tc.thread, tc.body), tc.name)
	}
}

// A channel line in a message body never stands in for the missing header
func TestChannelTitle_OnlyTheHeaderNamesTheChannel(t *testing.T) {
	r := answer(t, map[string]string{"messages": "=== Message from Kai (U2) at 2030-01-01 12:10:15 UTC ===\nMessage TS: 1893456100.000001\nChannel: #leads (C1)"})
	assert.Empty(t, slackmcp.ChannelTitle(r, "C1"))
}
