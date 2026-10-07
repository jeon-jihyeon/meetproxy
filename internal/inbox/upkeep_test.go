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

// A broken request file is moved aside and the rest still wait
func TestStoreList_Corrupt(t *testing.T) {
	t.Parallel()
	now := time.Now()
	dir := t.TempDir()
	s := inbox.New(dir)
	_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, now)
	require.NoError(t, err)
	broken := filepath.Join(dir, "inbox", inbox.IdOf(other)+".json")
	require.NoError(t, os.WriteFile(broken, []byte(`{"link":`), 0o600))

	waiting, err := s.Waiting(now)

	require.NoError(t, err)
	corrupt, cerr := s.Corrupt()
	require.NoError(t, cerr)
	_, statErr := os.Stat(broken)
	links := []string{}
	for _, it := range waiting {
		links = append(links, it.Link)
	}
	assert.Equal(t, []string{link}, links)
	assert.Equal(t, 1, corrupt)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestStoreTouch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		heartbeat time.Time
		waiting   bool
	}
	tcs := []struct {
		name    string
		session string
		takenAt time.Duration
		touches []time.Duration
		check   time.Duration
		want    want
	}{
		{"a heartbeat keeps a long take busy", "s1", -25 * time.Minute, []time.Duration{-10 * time.Minute}, 0, want{now.Add(-10 * time.Minute).UTC(), false}},
		{"a take with no heartbeat ages after twenty minutes", "s1", -25 * time.Minute, nil, 0, want{time.Time{}, true}},
		{"touches within five minutes write once", "s1", -10 * time.Minute, []time.Duration{-4 * time.Minute, -2 * time.Minute}, 0, want{now.Add(-4 * time.Minute).UTC(), false}},
		{"a fresh take needs no heartbeat", "s1", -time.Minute, []time.Duration{0}, 0, want{time.Time{}, false}},
		{"another session's touch leaves the take alone", "s2", -25 * time.Minute, []time.Duration{-10 * time.Minute}, 0, want{time.Time{}, true}},
		{"the heartbeat ages too", "s1", -50 * time.Minute, []time.Duration{-35 * time.Minute}, 0, want{now.Add(-35 * time.Minute).UTC(), true}},
		{"a take past twenty minutes is never revived", "s1", -25 * time.Minute, []time.Duration{0}, 0, want{time.Time{}, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, now.Add(tc.takenAt))
			require.NoError(t, err)
			_, err = s.Take(inbox.IdOf(link), "s1", now.Add(tc.takenAt))
			require.NoError(t, err)

			for _, at := range tc.touches {
				require.NoError(t, s.Touch(tc.session, now.Add(at)))
			}

			it, err := s.Get(inbox.IdOf(link))
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{it.HeartbeatAt, it.Waiting(now.Add(tc.check))})
		})
	}
}

func TestStoreExpire(t *testing.T) {
	t.Parallel()
	now := time.Now()
	day := 24 * time.Hour
	type want struct {
		status inbox.Status
		reason string
	}
	tcs := []struct {
		name   string
		status inbox.Status
		age    time.Duration
		want   want
	}{
		{"a new request past two weeks is closed", inbox.StatusNew, 15 * day, want{inbox.StatusDone, "expired"}},
		{"an ask request past two weeks is closed", inbox.StatusAsk, 15 * day, want{inbox.StatusDone, "expired"}},
		{"a new request of last week stays", inbox.StatusNew, 7 * day, want{inbox.StatusNew, ""}},
		{"a held request past two weeks stays", inbox.StatusHeld, 20 * day, want{inbox.StatusHeld, ""}},
		{"a held request past a month is closed", inbox.StatusHeld, 31 * day, want{inbox.StatusDone, "expired"}},
		{"a taken request is left to its session", inbox.StatusTaken, 31 * day, want{inbox.StatusTaken, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status}, now.Add(-tc.age))
			require.NoError(t, err)

			_, err = s.Expire(now)

			require.NoError(t, err)
			it, err := s.Get(inbox.IdOf(link))
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{it.Status, it.Reason})
		})
	}
}

func TestStoreLease(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ttl := 3 * time.Minute
	type hold struct {
		session string
		at      time.Duration
		drop    bool
	}
	tcs := []struct {
		name  string
		holds []hold
		want  []bool
	}{
		{"the first session holds it", []hold{{"s1", 0, false}}, []bool{true}},
		{"another session is refused while it lasts", []hold{{"s1", 0, false}, {"s2", time.Minute, false}}, []bool{true, false}},
		{"the holder renews it", []hold{{"s1", 0, false}, {"s1", 2 * time.Minute, false}, {"s2", 4 * time.Minute, false}}, []bool{true, true, false}},
		{"another session takes it once it ran out", []hold{{"s1", 0, false}, {"s2", 4 * time.Minute, false}, {"s1", 5 * time.Minute, false}}, []bool{true, true, false}},
		{"a dropped lease goes to the next session at once", []hold{{"s1", 0, false}, {"s1", 0, true}, {"s2", time.Second, false}}, []bool{true, true, true}},
		{"a drop by another session leaves it", []hold{{"s1", 0, false}, {"s2", 0, true}, {"s2", time.Second, false}}, []bool{true, true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			got := make([]bool, 0, len(tc.holds))
			for _, h := range tc.holds {
				if h.drop {
					require.NoError(t, s.Drop("slack", h.session))
					got = append(got, true)
					continue
				}
				held, err := s.Lease("slack", h.session, ttl, now.Add(h.at))
				require.NoError(t, err)
				got = append(got, held)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStoreLease_BadSource(t *testing.T) {
	t.Parallel()
	_, err := inbox.New(t.TempDir()).Lease("../x", "s1", time.Minute, time.Now())
	assert.Error(t, err)
}
