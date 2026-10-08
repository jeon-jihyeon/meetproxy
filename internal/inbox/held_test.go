package inbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

// A held request comes back as open once its time came
func TestStoreHold_Until(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		due  bool
		open bool
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
			_, _, err := add(s, inbox.Item{Link: link}, now)
			require.NoError(t, err)
			_, err = s.Hold(inbox.IdOf(link), "s1", tc.until, now)
			require.NoError(t, err)

			it, err := s.Get(inbox.IdOf(link))

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{it.Due(tc.at), it.Open(tc.at)})
		})
	}
}

// A question to the requester comes back to the user as open once three days pass with no reply
func TestStoreQuestion(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		open   bool
		status inbox.Status
		reason string
	}
	tcs := []struct {
		name  string
		after time.Duration
		want  want
	}{
		{"a fresh question waits for the requester", time.Hour, want{false, inbox.StatusQuestion, ""}},
		{"a question left three days opens again", 72 * time.Hour, want{true, inbox.StatusOpen, inbox.Unanswered}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			it := seed(t, s, inbox.Item{Link: link}, inbox.StatusQuestion, now)

			at := now.Add(tc.after)
			all, err := s.List()

			require.NoError(t, err)
			waiting := inbox.Waiting(all, at)
			require.Len(t, waiting, 1)
			assert.Equal(t, tc.want, want{it.Open(at), waiting[0].Status, waiting[0].Reason})
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
			}, "200", want{inbox.StatusOpen, time.Time{}, true, true},
		},
		{
			"a newer message on a held request clears its time", func(s inbox.Store) error {
				_, err := s.Hold(inbox.IdOf(link), "s1", now.Add(time.Hour), now)
				return err
			}, "200", want{inbox.StatusOpen, time.Time{}, true, true},
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
			}, "200", want{inbox.StatusOpen, time.Time{}, true, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := add(s, inbox.Item{Link: link, Ts: "100"}, now)
			require.NoError(t, err)
			require.NoError(t, tc.settle(s))

			_, created, err := add(s, inbox.Item{Link: link, Ts: tc.ts, Followup: true}, now)

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
	const next = "https://w.slack.com/archives/C1/p9"
	tcs := []struct {
		name  string
		lim   inbox.Limits
		item  inbox.Item
		after time.Duration
		want  error
	}{
		{"a request under the limits is queued", inbox.Limits{PerHour: 3, Dedupe: time.Minute},
			inbox.Item{Link: next, Delegation: "ops", Digest: "b"}, 0, nil},
		{"the limit of the hour refuses one more", inbox.Limits{PerHour: 2},
			inbox.Item{Link: next, Delegation: "ops", Digest: "b"}, 0, inbox.ErrLimited},
		{"requests past the hour no longer count", inbox.Limits{PerHour: 2},
			inbox.Item{Link: next, Delegation: "ops", Digest: "b"}, 55 * time.Minute, nil},
		{"the same digest within the window is a duplicate", inbox.Limits{Dedupe: time.Hour},
			inbox.Item{Link: next, Delegation: "ops", Digest: "a"}, 0, inbox.ErrDuplicate},
		{"the same digest past the window is queued", inbox.Limits{Dedupe: 5 * time.Minute},
			inbox.Item{Link: next, Delegation: "ops", Digest: "a"}, 0, nil},
		{"another delegation counts on its own", inbox.Limits{PerHour: 2, Dedupe: time.Hour},
			inbox.Item{Link: next, Delegation: "dev", Digest: "a"}, 0, nil},
		{"a thread seen before is never limited", inbox.Limits{PerHour: 1, Dedupe: time.Hour},
			inbox.Item{Link: link, Delegation: "ops", Digest: "a", Ts: "5"}, 0, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, l := range []string{link, other} {
				it := inbox.Item{Link: l, Delegation: "ops", Digest: "a"}
				_, _, err := s.AddLimited(it, inbox.Limits{PerHour: 100}, now.Add(-10*time.Minute))
				require.NoError(t, err)
			}

			_, _, err := s.AddLimited(tc.item, tc.lim, now.Add(tc.after))

			assert.ErrorIs(t, err, tc.want)
			if tc.want == nil {
				assert.NoError(t, err)
			}
		})
	}
}

// Each request queued under limits counts toward the next one
func TestStoreAddLimited_Counts(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	lim := inbox.Limits{PerHour: 2}
	got := []error{}
	for i, l := range []string{link, other, "https://w.slack.com/archives/C1/p9"} {
		_, _, err := s.AddLimited(inbox.Item{Link: l, Delegation: "ops"}, lim, now.Add(time.Duration(i)*time.Minute))
		got = append(got, err)
	}

	assert.Equal(t, []error{nil, nil, inbox.ErrLimited}, got)
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
		{"a handoff reaction is refused", []string{"handoff"}, nil, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := add(s, inbox.Item{Link: link}, time.Now())
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
