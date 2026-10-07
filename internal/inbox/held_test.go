package inbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

// A held request comes back as due once its time came and a session may claim it then
func TestStoreHold_Until(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		due     bool
		claimed bool
	}
	tcs := []struct {
		name  string
		until time.Time
		at    time.Time
		want  want
	}{
		{"before its time it waits", now.Add(time.Hour), now, want{false, false}},
		{"at its time it is due", now.Add(time.Hour), now.Add(time.Hour), want{true, true}},
		{"without a time it waits for the user", time.Time{}, now.Add(48 * time.Hour), want{false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusAsk}, now)
			require.NoError(t, err)
			_, err = s.Hold(inbox.IdOf(link), "s1", tc.until, now)
			require.NoError(t, err)

			waiting, err := s.Waiting(tc.at)
			require.NoError(t, err)
			_, claimed, _ := s.Claim(inbox.IdOf(link), "s2", tc.at)

			require.Len(t, waiting, 1)
			assert.Equal(t, tc.want, want{waiting[0].Due(tc.at), claimed})
		})
	}
}

// A question to the requester never waits for the user until three days pass with no reply
func TestStoreQuestion(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		status  inbox.Status
		reason  string
		claimed bool
	}
	tcs := []struct {
		name  string
		after time.Duration
		want  want
	}{
		{"a fresh question waits for the requester", time.Hour, want{inbox.StatusQuestion, "", false}},
		{"a question left three days is asked to the user", 72 * time.Hour, want{inbox.StatusAsk, inbox.Unanswered, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, now)
			require.NoError(t, err)
			_, err = s.Take(inbox.IdOf(link), "s1", now)
			require.NoError(t, err)
			_, err = s.Question(inbox.IdOf(link), "s1", now)
			require.NoError(t, err)

			at := now.Add(tc.after)
			waiting, err := s.Waiting(at)
			require.NoError(t, err)
			_, claimed, _ := s.Claim(inbox.IdOf(link), "s2", at)

			require.Len(t, waiting, 1)
			assert.Equal(t, tc.want, want{waiting[0].Status, waiting[0].Reason, claimed})
		})
	}
}

// A newer message on a request that waits reopens it with the new verdict
func TestStoreAdd_Reopens(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		status   inbox.Status
		until    time.Time
		followup bool
		created  bool
	}
	tcs := []struct {
		name   string
		settle func(s inbox.Store) error
		ts     string
		want   want
	}{
		{
			"a reply to a question reopens it", func(s inbox.Store) error {
				if _, err := s.Take(inbox.IdOf(link), "s1", now); err != nil {
					return err
				}
				_, err := s.Question(inbox.IdOf(link), "s1", now)
				return err
			}, "200", want{inbox.StatusNew, time.Time{}, true, true},
		},
		{
			"a newer message on a held request clears its time", func(s inbox.Store) error {
				_, err := s.Hold(inbox.IdOf(link), "s1", now.Add(time.Hour), now)
				return err
			}, "200", want{inbox.StatusNew, time.Time{}, true, true},
		},
		{
			"an older message leaves a held request held", func(s inbox.Store) error {
				_, err := s.Hold(inbox.IdOf(link), "s1", now.Add(time.Hour), now)
				return err
			}, "50", want{inbox.StatusHeld, now.Add(time.Hour).UTC(), false, false},
		},
		{
			"a follow-up of an answered request is stored anew", func(s inbox.Store) error {
				_, err := s.Done(inbox.IdOf(link), "s1", now)
				return err
			}, "200", want{inbox.StatusNew, time.Time{}, true, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "100"}, now)
			require.NoError(t, err)
			require.NoError(t, tc.settle(s))

			_, created, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew, Ts: tc.ts, Followup: true}, now)

			require.NoError(t, err)
			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, want{it.Status, it.HeldUntil, it.Followup, created})
		})
	}
}

func TestStoreAddLimited(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name string
		lim  inbox.Limits
		item inbox.Item
		want error
	}{
		{"a request under the limits is queued", inbox.Limits{PerHour: 3, Dedupe: time.Minute}, inbox.Item{Link: "https://w.slack.com/archives/C1/p9", Delegation: "ops", Digest: "b"}, nil},
		{"the limit of the hour refuses one more", inbox.Limits{PerHour: 2}, inbox.Item{Link: "https://w.slack.com/archives/C1/p9", Delegation: "ops", Digest: "b"}, inbox.ErrLimited},
		{"the same digest within the window is a duplicate", inbox.Limits{Dedupe: time.Hour}, inbox.Item{Link: "https://w.slack.com/archives/C1/p9", Delegation: "ops", Digest: "a"}, inbox.ErrDuplicate},
		{"another delegation counts on its own", inbox.Limits{PerHour: 2, Dedupe: time.Hour}, inbox.Item{Link: "https://w.slack.com/archives/C1/p9", Delegation: "dev", Digest: "a"}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, l := range []string{link, other} {
				_, _, err := s.Add(inbox.Item{Link: l, Status: inbox.StatusNew, Delegation: "ops", Digest: "a"}, now.Add(-10*time.Minute))
				require.NoError(t, err)
			}
			tc.item.Status = inbox.StatusNew

			_, _, err := s.AddLimited(tc.item, tc.lim, now)

			assert.ErrorIs(t, err, tc.want)
			if tc.want == nil {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStoreIgnored(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	require.NoError(t, s.Ignore(inbox.Ignored{Link: link, Reason: "thanks"}, now.Add(-4*24*time.Hour)))
	require.NoError(t, s.Ignore(inbox.Ignored{Link: other, From: "U2", Delegation: "default", Reason: "fyi"}, now.Add(-time.Hour)))

	before, err := s.IgnoredSince(now, 0)
	require.NoError(t, err)
	require.NoError(t, s.PruneIgnored(now))
	after, err := s.IgnoredSince(now.Add(-5*24*time.Hour), 0)
	require.NoError(t, err)

	assert.Equal(t, []string{other}, links(before))
	assert.Equal(t, []string{other}, links(after), "pruning drops records past three days")
}

func links(rs []inbox.Ignored) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Link)
	}
	return out
}

func TestStoreAck(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		reacts []string
		want   []string
		failed bool
	}{
		{"a reaction is recorded once", []string{inbox.AckSeen, inbox.AckSeen, inbox.AckDone}, []string{inbox.AckSeen, inbox.AckDone}, false},
		{"an unknown reaction is refused", []string{"thumbsup"}, nil, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, time.Now())
			require.NoError(t, err)
			var failed bool
			for _, r := range tc.reacts {
				_, err := s.Ack(inbox.IdOf(link), r)
				failed = failed || err != nil
			}
			it, err := s.Get(inbox.IdOf(link))
			require.NoError(t, err)
			assert.Equal(t, tc.want, it.Acked)
			assert.Equal(t, tc.failed, failed)
		})
	}
}

// Claim records whether the session answers alone or after asking
func TestStoreClaim_Mode(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		status inbox.Status
		want   string
	}{
		{"a new request is answered alone", inbox.StatusNew, inbox.ModeAuto},
		{"a request for the user is answered after asking", inbox.StatusAsk, inbox.ModeAsked},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status}, now)
			require.NoError(t, err)
			_, ok, err := s.Claim(inbox.IdOf(link), "s1", now)
			require.NoError(t, err)
			require.True(t, ok)

			it, err := s.Take(inbox.IdOf(link), "s1", now)

			require.NoError(t, err)
			assert.Equal(t, tc.want, it.Mode, "a take after the claim keeps its mode")
		})
	}
}
