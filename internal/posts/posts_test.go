package posts_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/posts"
)

const (
	request = "3632439ab676"
	origin  = "https://w.slack.com/archives/C1/p1893456100000001"
	reply   = "https://w.slack.com/archives/C1/p1893456150000001?thread_ts=1893456100.000001&cid=C1"
)

func post(reply string) posts.Post {
	return posts.Post{Request: request, Origin: origin, Reply: reply, Body: "in config.go", Mode: posts.ModeInbox, Kind: posts.KindAnswer}
}

func TestStoreAdd(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name   string
		p      posts.Post
		seen   string
		failed bool
	}{
		{"a reply is recorded and watched", post(reply), "1893456150.000001", false},
		{"a post without a reply link is refused", post(""), "1", true},
		{"a request id that is no inbox id is refused", posts.Post{Request: "../x", Reply: reply}, "1", true},
		{"seen must be unix seconds", post(reply), "soon", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := posts.New(t.TempDir())

			err := s.Add(tc.p, tc.seen, now)

			assert.Equal(t, tc.failed, err != nil, err)
			ws, werr := s.Watches(now)
			require.NoError(t, werr)
			ps, lerr := s.List(0)
			require.NoError(t, lerr)
			if tc.failed {
				assert.Empty(t, ws)
				assert.Empty(t, ps)
				return
			}
			assert.Equal(t, []posts.Watch{{Request: request, Origin: origin, Thread: origin, Seen: tc.seen, Until: now.Add(72 * time.Hour).UTC()}}, ws)
			assert.Equal(t, []string{reply}, []string{ps[0].Reply})
		})
	}
}

func TestStoreAdd_ClipsBody(t *testing.T) {
	t.Parallel()
	s := posts.New(t.TempDir())
	p := post(reply)
	p.Body = string(make([]rune, posts.MaxBody+10))
	require.NoError(t, s.Add(p, "1", time.Now()))
	got, err := s.Find(reply)
	require.NoError(t, err)
	assert.Len(t, []rune(got.Body), posts.MaxBody)
}

func TestStoreList(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := posts.New(t.TempDir())
	for i, r := range []string{"https://github.com/o/r/pull/1#issuecomment-1", "https://github.com/o/r/pull/1#issuecomment-2", "https://github.com/o/r/pull/1#issuecomment-3"} {
		require.NoError(t, s.Add(post(r), "1", now.Add(time.Duration(i)*time.Minute)))
	}

	all, err := s.List(0)
	require.NoError(t, err)
	two, err := s.List(2)
	require.NoError(t, err)

	assert.Equal(t, []string{"https://github.com/o/r/pull/1#issuecomment-3", "https://github.com/o/r/pull/1#issuecomment-2", "https://github.com/o/r/pull/1#issuecomment-1"}, replies(all))
	assert.Equal(t, replies(all)[:2], replies(two))
}

func replies(ps []posts.Post) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Reply)
	}
	return out
}

func TestStoreRetract(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name   string
		reply  string
		failed bool
	}{
		{"a reply of the ledger is retracted", reply, false},
		{"a reply outside the ledger is refused", "https://w.slack.com/archives/C1/p1", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := posts.New(t.TempDir())
			require.NoError(t, s.Add(post(reply), "1", now))

			_, err := s.Retract(tc.reply, now.Add(time.Minute))

			assert.Equal(t, tc.failed, err != nil)
			got, ferr := s.Find(reply)
			require.NoError(t, ferr)
			posted, perr := s.Posted(request)
			require.NoError(t, perr)
			assert.Equal(t, tc.failed, got.RetractedAt.IsZero())
			assert.Equal(t, tc.failed, posted, "a retracted reply no longer counts as an answer")
			all, lerr := s.List(0)
			require.NoError(t, lerr)
			assert.Len(t, all, 1, "the retraction replaces the record of the reply")
		})
	}
}

func TestStoreWatches(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name  string
		after time.Duration
		seen  []string
		want  []string
	}{
		{"seen moves forward", time.Hour, []string{"1893456200.000001"}, []string{"1893456200.000001"}},
		{"seen never moves back", time.Hour, []string{"1893456200.000001", "1893456160"}, []string{"1893456200.000001"}},
		{"a watch past three days is dropped", 73 * time.Hour, nil, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := posts.New(t.TempDir())
			require.NoError(t, s.Add(post(reply), "1893456150.000001", now))
			for _, ts := range tc.seen {
				require.NoError(t, s.Seen(request, ts))
			}

			ws, err := s.Watches(now.Add(tc.after))

			require.NoError(t, err)
			seen := []string{}
			for _, w := range ws {
				seen = append(seen, w.Seen)
			}
			assert.Equal(t, tc.want, seen)
		})
	}
}

func TestStorePrune(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := posts.New(t.TempDir())
	require.NoError(t, s.Add(post("https://github.com/o/r/pull/1#issuecomment-1"), "1", now.Add(-31*24*time.Hour)))
	require.NoError(t, s.Add(post(reply), "1", now.Add(-time.Hour)))

	require.NoError(t, s.Prune(now))

	all, err := s.List(0)
	require.NoError(t, err)
	assert.Equal(t, []string{reply}, replies(all))
}
