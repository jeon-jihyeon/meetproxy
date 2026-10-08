package inbox_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

// A newer message is the request now unless a session works on it
func TestStoreAdd_Merge(t *testing.T) {
	t.Parallel()
	now := time.Now()
	first := inbox.Item{
		Thread: "C1/1", Link: link, Ts: "1", Delegation: "default", Task: "answer", Depth: "quick", Digest: "a",
	}
	newer := inbox.Item{
		Thread: "C1/1", Link: other, Ts: "2", Delegation: "pr", Task: "review", Target: "https://github.com/o/r/pull/1",
		MayApprove: true, Depth: "deep", Digest: "b",
	}
	type want struct {
		link       string
		delegation string
		task       string
		target     string
		mayApprove bool
		depth      string
		digest     string
		status     inbox.Status
		again      bool
	}
	asked := func(link, digest string) want {
		return want{link, "pr", "review", "https://github.com/o/r/pull/1", true, "deep", digest, inbox.StatusOpen, false}
	}
	tcs := []struct {
		name   string
		status inbox.Status
		want   want
	}{
		{"an open request takes the task and keeps its link and digest", inbox.StatusOpen, asked(link, "a")},
		{"a held request opens as the newer message asks", inbox.StatusHeld, asked(link, "b")},
		{"a question opens as the newer message asks", inbox.StatusQuestion, asked(link, "b")},
		{"a done request also moves its link to the newer message", inbox.StatusDone, asked(other, "b")},
		{
			"a taken request keeps its task", inbox.StatusTaken,
			want{link, "default", "answer", "", false, "quick", "a", inbox.StatusTaken, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, first, tc.status, now)

			_, _, err := add(s, newer, now)

			require.NoError(t, err)
			it, err := s.Get(inbox.IdOf("C1/1"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{
				it.Link, it.Delegation, it.Task, it.Target, it.MayApprove, it.Depth, it.Digest, it.Status, it.Again,
			})
		})
	}
}

// Adding leaves old done requests to Prune so it never reads the whole inbox
func TestStorePrune(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const recent = "https://w.slack.com/archives/C1/p9"
	type want struct {
		ids   []string
		beats []string
	}
	tcs := []struct {
		name  string
		prune bool
		want  want
	}{
		{
			"adding keeps every request and heartbeat", false,
			want{[]string{inbox.IdOf(other), inbox.IdOf(recent), inbox.IdOf(link)}, []string{"old", "s-1"}},
		},
		{
			"pruning drops done requests past a week and heartbeats past a day", true,
			want{[]string{inbox.IdOf(other), inbox.IdOf(recent)}, []string{"s-1"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := inbox.New(dir)
			seed(t, s, inbox.Item{Link: link}, inbox.StatusDone, now.Add(-8*24*time.Hour))
			seed(t, s, inbox.Item{Link: recent}, inbox.StatusDone, now.Add(-24*time.Hour))
			require.NoError(t, s.Beat("old", now.Add(-25*time.Hour)))
			require.NoError(t, s.Beat("s/1", now.Add(-time.Hour)))
			_, _, err := add(s, inbox.Item{Link: other}, now)
			require.NoError(t, err)

			if tc.prune {
				require.NoError(t, s.Prune(now))
			}

			items, err := s.List()
			require.NoError(t, err)
			ids := make([]string, 0, len(items))
			for _, it := range items {
				ids = append(ids, it.Id)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "inbox", "beat"))
			require.NoError(t, err)
			beats := make([]string, 0, len(entries))
			for _, e := range entries {
				beats = append(beats, e.Name())
			}
			assert.Equal(t, tc.want, want{ids, beats})
		})
	}
}

// A take of a session that sent heartbeats opens again once they stop
// A session that never sent one keeps the day long rule of watchers before heartbeats
func TestStoreBeat(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type args struct {
		// Offsets of the heartbeats of the taking session from the take
		beats []time.Duration
		after time.Duration
	}
	type want struct {
		waiting bool
		takenBy bool
		status  inbox.Status
		reason  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a take with no heartbeat lasts an hour", args{nil, time.Hour}, want{false, true, inbox.StatusTaken, ""}},
		{
			"a take with no heartbeat ends after a day", args{nil, 25 * time.Hour},
			want{true, false, inbox.StatusOpen, inbox.Released},
		},
		{
			"a take lasts while heartbeats come",
			args{[]time.Duration{-time.Minute, 50 * time.Minute, 59 * time.Minute}, time.Hour},
			want{false, true, inbox.StatusTaken, ""},
		},
		{
			"a take opens again once heartbeats stop but its session keeps its scope",
			args{[]time.Duration{-time.Minute, 10 * time.Minute}, 13 * time.Minute},
			want{true, true, inbox.StatusOpen, inbox.Released},
		},
		{
			"a fresh take lasts past an old heartbeat", args{[]time.Duration{-time.Hour}, 2 * time.Minute},
			want{false, true, inbox.StatusTaken, ""},
		},
		{
			"a take opens again soon after an old heartbeat", args{[]time.Duration{-time.Hour}, 3 * time.Minute},
			want{true, true, inbox.StatusOpen, inbox.Released},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			id := seed(t, s, inbox.Item{Link: link}, inbox.StatusTaken, now).Id
			// Heartbeats of another session never keep this take
			require.NoError(t, s.Beat("s2", now.Add(tc.args.after)))
			for _, at := range tc.args.beats {
				require.NoError(t, s.Beat("s1", now.Add(at)))
			}
			at := now.Add(tc.args.after)

			it, err := s.Get(id)
			require.NoError(t, err)
			_, takenBy, err := s.TakenBy("s1", at)
			require.NoError(t, err)
			all, err := s.List()
			require.NoError(t, err)
			shown := inbox.Item{Status: inbox.StatusTaken}
			if waiting := inbox.Waiting(all, at); len(waiting) == 1 {
				shown = waiting[0]
			}

			assert.Equal(t, tc.want, want{it.Waiting(at), takenBy, shown.Status, shown.Reason})
		})
	}
}

// A heartbeat that stopped lets another session take the request over
func TestStoreBeat_Takeover(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	id := seed(t, s, inbox.Item{Link: link}, inbox.StatusTaken, now).Id
	require.NoError(t, s.Beat("s1", now))

	_, early := s.Take(id, "s2", now.Add(2*time.Minute))
	it, late := s.Take(id, "s2", now.Add(4*time.Minute))

	assert.ErrorIs(t, early, inbox.ErrTaken)
	require.NoError(t, late)
	assert.Equal(t, "s2", it.SessionId)
}

func TestStoreBeat_NoSession(t *testing.T) {
	t.Parallel()
	assert.Error(t, inbox.New(t.TempDir()).Beat("", time.Now()))
}

func TestItemLast(t *testing.T) {
	t.Parallel()
	added := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		ts   string
		want time.Time
	}{
		{"a Slack timestamp keeps its microseconds", "1893456000.000123", time.Unix(1893456000, 123000).UTC()},
		{"whole seconds", "1893456000", time.Unix(1893456000, 0).UTC()},
		{"digits past nanoseconds are dropped", "1893456000.0000000019", time.Unix(1893456000, 1).UTC()},
		{"no timestamp falls back to when it was added", "", added},
		{"a malformed timestamp falls back to when it was added", "soon", added},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, inbox.Item{Ts: tc.ts, AddedAt: added}.Last())
		})
	}
}

func TestStoreKnown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const ignored = "https://w.slack.com/archives/C1/p7"
	type args struct {
		thread string
		link   string
		ts     string
	}
	type want struct {
		known  bool
		failed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the newest message of a thread", args{"C1/1", other, "1800000000.000002"}, want{true, false}},
		{"an older message of a thread", args{"C1/1", other, "1800000000.000001"}, want{true, false}},
		{"a newer message of a thread", args{"C1/1", other, "1800000000.000003"}, want{false, false}},
		{"a thread the inbox never saw", args{"C1/9", other, "1800000000.000001"}, want{false, false}},
		{"a request keyed by its link", args{"", link, "1800000000.000002"}, want{true, false}},
		{"an ignored message", args{"C1/9", ignored, "1800000000.000005"}, want{true, false}},
		{"no timestamp only checks the ignored list", args{"C1/1", other, ""}, want{false, false}},
		{"a timestamp that is not unix seconds", args{"C1/1", other, "soon"}, want{false, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Thread: "C1/1", Link: other, Ts: "1800000000.000002"}, inbox.StatusDone, now)
			seed(t, s, inbox.Item{Link: link, Ts: "1800000000.000002"}, inbox.StatusOpen, now)
			require.NoError(t, s.Ignore(inbox.Ignored{Link: ignored, Reason: "fyi"}, now))

			known, err := s.Known(tc.args.thread, tc.args.link, tc.args.ts, now)

			assert.Equal(t, tc.want, want{known, err != nil})
		})
	}
}

func TestStoreLeases(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	_, err := s.Lease("slack", "s1", 3*time.Minute, now)
	require.NoError(t, err)
	_, err = s.Lease("github", "s2", time.Minute, now.Add(-2*time.Minute))
	require.NoError(t, err)

	leases, err := s.Leases(now)

	require.NoError(t, err)
	assert.Equal(t, map[string]inbox.Lease{"slack": {Session: "s1", Until: now.Add(3 * time.Minute).UTC()}}, leases,
		"a lease that ran out is left out")
}
