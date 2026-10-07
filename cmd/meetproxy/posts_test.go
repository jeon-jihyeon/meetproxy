package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

// The steps run in order on one data directory as sessions would
func TestRun_Posts(t *testing.T) {
	t.Parallel()
	now := time.Unix(1893456400, 0)
	data := t.TempDir()
	origin := "https://w.slack.com/archives/C1/p1893456100000001"
	reply := "https://w.slack.com/archives/C1/p1893456150000001?thread_ts=1893456100.000001&cid=C1"
	id := inbox.IdOf(origin)
	ops := `{"id":"ops","when":"channel","channel":"C9","host":"w.slack.com","do":"investigate","max_per_hour":1}`
	alertLink := func(n string) string { return "https://w.slack.com/archives/C9/p189345600000000" + n }
	alert := func(n string) step {
		return step{"inbox", []string{"add", alertLink(n), "--delegation", "ops", "--ts", "1893456000.00000" + n, "--key", "ops"}, ""}
	}
	steps := []struct {
		name string
		run  step
		want slackWant
	}{
		{"the protocol is 9", step{"protocol", nil, ""}, slackWant{0, "9", false}},
		{"a channel delegation with a limit", step{"delegation", []string{"put"}, ops}, slackWant{0, "", false}},
		{"the first alert is queued", alert("1"), slackWant{0, inbox.IdOf(alertLink("1")) + "\topen\tnew", false}},
		{
			"one past the limit is ignored and moves the cursor", alert("2"),
			slackWant{0, alertLink("2") + "\tignored\t" + inbox.ErrLimited.Error(), false},
		},
		{"the cursor moved past it", step{"inbox", []string{"cursor", "--key", "ops"}, ""}, slackWant{0, "1893456000.000002", false}},
		{"a post outside a scope is not recorded", step{"posts", []string{"add"}, `{"reply":"` + reply + `","body":"x"}`}, slackWant{1, "", true}},
		{
			"the request is queued", step{"inbox", []string{"add", origin, "--ts", "1893456100.000001"}, ""},
			slackWant{0, id + "\topen\tnew", false},
		},
		{"the session takes it", step{"inbox", []string{"take", id}, ""}, slackWant{0, origin + "\tanswer\t-\tno\tquick\t-", false}},
		{"usage error for another kind", step{"posts", []string{"add"}, `{"reply":"` + reply + `","kind":"poem"}`}, slackWant{exitUsage, "", true}},
		{"a post in the scope is recorded", step{"posts", []string{"add"}, `{"reply":"` + reply + `","body":"in config.go","kind":"question"}`}, slackWant{0, "recorded " + id, false}},
		{
			"the ledger lists it", step{"posts", []string{"list", "--limit", "1"}, ""},
			slackWant{0, `[{"request":"` + id + `","origin":"` + origin + `","reply":"` + reply + `","body":"in config.go","delegation":"default","mode":"inbox","kind":"question","session":"s1","at":"2030-01-01T00:06:40Z"}]`, false},
		},
		{"the session asks the requester back", step{"inbox", []string{"question", id}, ""}, slackWant{0, "", false}},
		{"the watch moves past a reply", step{"watch", []string{"seen", id, "1893456200.000001"}, ""}, slackWant{0, "", false}},
		{"usage error for a hold time that is none", step{"inbox", []string{"hold", id, "--until", "someday"}, ""}, slackWant{exitUsage, "", true}},
		{"a reply outside the ledger is not found", step{"posts", []string{"find", origin}, ""}, slackWant{1, "", true}},
		{"a reply of the ledger is retracted", step{"posts", []string{"retract", reply}, ""}, slackWant{0, "retracted " + reply, false}},
		{"an ignored message is recorded", step{"inbox", []string{"ignore", "https://w.slack.com/archives/C1/p3", "--reason", "thanks", "--ts", "1893456300", "--key", "mention"}, ""}, slackWant{0, "", false}},
		{"the request is acknowledged", step{"inbox", []string{"acked", id, "--react", "eyes"}, ""}, slackWant{0, "", false}},
		{"usage of an unknown reaction fails", step{"inbox", []string{"acked", id, "--react", "wave"}, ""}, slackWant{1, "", true}},
		{"handoff is no reaction any more", step{"inbox", []string{"acked", id, "--react", "handoff"}, ""}, slackWant{1, "", true}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(s.run.cmd, append([]string{"--data", data}, s.run.args...), "s1", now, strings.NewReader(s.run.stdin), &out)
			assert.Equal(t, s.want.code, code)
			assert.Equal(t, s.want.failed, err != nil, err)
			assert.Equal(t, s.want.out, strings.TrimSpace(out.String()))
		})
	}

	t.Run("tick names the watch and the reactions owed", func(t *testing.T) {
		var out bytes.Buffer
		_, err := run("tick", []string{"--data", data}, "s1", now, nil, &out)
		require.NoError(t, err)
		var got tick
		require.NoError(t, json.Unmarshal(out.Bytes(), &got))
		require.Len(t, got.Watch, 1)
		assert.Equal(t, []string{id, origin, "1893456200.000001"}, []string{got.Watch[0].Request, got.Watch[0].Thread, got.Watch[0].Seen})
		statuses := map[string]inbox.Status{}
		for _, r := range got.Waiting {
			statuses[r.Id] = r.Status
		}
		assert.Equal(t, inbox.StatusQuestion, statuses[id])
		for _, a := range got.Acks {
			assert.False(t, a.Id == id && a.React == inbox.AckSeen, "eyes was added once")
		}
	})
	t.Run("status counts the history", func(t *testing.T) {
		var out bytes.Buffer
		_, err := run("status", []string{"--data", data}, "s1", now, nil, &out)
		require.NoError(t, err)
		var st status
		require.NoError(t, json.Unmarshal(out.Bytes(), &st))
		assert.Equal(t, history{Posts: 1, Retracted: 1, Ignored: 2, Watching: 1}, st.History)
		assert.Equal(t, 1, st.Inbox.Question)
		assert.Equal(t, []heldRow{}, st.Held)
	})
}

func TestHoldUntil(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 1, 15, 0, 0, 0, time.Local)
	tcs := []struct {
		name   string
		in     string
		want   time.Time
		failed bool
	}{
		{"empty waits for the user", "", time.Time{}, false},
		{"a duration from now", "1h", now.Add(time.Hour), false},
		{"tomorrow at nine", "tomorrow", time.Date(2030, 1, 2, 9, 0, 0, 0, time.Local), false},
		{"an RFC 3339 time", "2030-01-03T10:00:00Z", time.Date(2030, 1, 3, 10, 0, 0, 0, time.UTC), false},
		{"a negative duration is refused", "-1h", time.Time{}, true},
		{"anything else is refused", "soon", time.Time{}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := holdUntil(tc.in, now)
			assert.Equal(t, tc.failed, err != nil)
			assert.True(t, tc.want.Equal(got), got)
		})
	}
}
