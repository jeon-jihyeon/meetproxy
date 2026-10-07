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
	_, _, err := s.Add(inbox.Item{Link: link}, now)
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
		{"an open request past two weeks is closed", inbox.StatusOpen, 15 * day, want{inbox.StatusDone, "expired"}},
		{"a question past two weeks is closed", inbox.StatusQuestion, 15 * day, want{inbox.StatusDone, "expired"}},
		{"an open request of last week stays", inbox.StatusOpen, 7 * day, want{inbox.StatusOpen, ""}},
		{"a question of last week stays", inbox.StatusQuestion, 7 * day, want{inbox.StatusQuestion, ""}},
		{"a held request past two weeks stays", inbox.StatusHeld, 20 * day, want{inbox.StatusHeld, ""}},
		{"a held request past a month is closed", inbox.StatusHeld, 31 * day, want{inbox.StatusDone, "expired"}},
		{"a taken request is left to its session", inbox.StatusTaken, 31 * day, want{inbox.StatusTaken, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: link}, tc.status, now.Add(-tc.age))

			_, err := s.Expire(now)

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
