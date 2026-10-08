package inbox_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
)

const (
	link  = "https://w.slack.com/archives/C1/p1"
	other = "https://w.slack.com/archives/C1/p2"
)

// Stores a request the way a delegation without limits would
func add(s inbox.Store, it inbox.Item, at time.Time) (inbox.Item, bool, error) {
	return s.AddLimited(it, inbox.Limits{}, at)
}

// Stores a request and brings it to status the way session s1 would
func seed(t *testing.T, s inbox.Store, it inbox.Item, status inbox.Status, at time.Time) inbox.Item {
	t.Helper()
	stored, _, err := add(s, it, at)
	require.NoError(t, err)
	switch status {
	case inbox.StatusTaken:
		stored, err = s.Take(stored.Id, "s1", at)
	case inbox.StatusHeld:
		stored, err = s.Hold(stored.Id, "s1", time.Time{}, at)
	case inbox.StatusQuestion:
		if stored, err = s.Take(stored.Id, "s1", at); err == nil {
			stored, err = s.Question(stored.Id, "s1", at)
		}
	case inbox.StatusDone:
		stored, err = s.Done(stored.Id, "s1", at)
	}
	require.NoError(t, err)
	return stored
}

func TestStoreAdd(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type stored struct {
		item   inbox.Item
		status inbox.Status
	}
	type want struct {
		added  bool
		status inbox.Status
		reason string
		ts     string
		again  bool
		failed bool
		count  int
	}
	tcs := []struct {
		name   string
		first  stored
		paused bool
		add    inbox.Item
		want   want
	}{
		{
			"stores a new thread as open",
			stored{inbox.Item{Link: other}, inbox.StatusOpen}, false,
			inbox.Item{Link: link, Status: inbox.StatusDone},
			want{true, inbox.StatusOpen, "", "", false, false, 2},
		},
		{
			"keeps a thread seen before",
			stored{inbox.Item{Link: link, Reason: "a"}, inbox.StatusOpen}, false,
			inbox.Item{Link: link, Reason: "b"},
			want{false, inbox.StatusOpen, "a", "", false, false, 1},
		},
		{
			"keeps an open request asked again with its newest timestamp",
			stored{inbox.Item{Link: link, Ts: "1"}, inbox.StatusOpen}, false,
			inbox.Item{Link: link, Ts: "2"},
			want{false, inbox.StatusOpen, "", "2", false, false, 1},
		},
		{
			"keeps the timestamp of an open request found by an older scan",
			stored{inbox.Item{Link: link, Ts: "2"}, inbox.StatusOpen}, false,
			inbox.Item{Link: link, Ts: "1"},
			want{false, inbox.StatusOpen, "", "2", false, false, 1},
		},
		{
			"marks a taken request asked again",
			stored{inbox.Item{Link: link, Ts: "1"}, inbox.StatusTaken}, false,
			inbox.Item{Link: link, Reason: "new", Ts: "2"},
			want{false, inbox.StatusTaken, "", "2", true, false, 1},
		},
		{
			"reopens a held request asked again",
			stored{inbox.Item{Link: link, Reason: "old", Ts: "1"}, inbox.StatusHeld}, false,
			inbox.Item{Link: link, Reason: "new", Ts: "2"},
			want{true, inbox.StatusOpen, "new", "2", false, false, 1},
		},
		{
			"reopens a question asked again",
			stored{inbox.Item{Link: link, Reason: "old", Ts: "1"}, inbox.StatusQuestion}, false,
			inbox.Item{Link: link, Reason: "new", Ts: "2"},
			want{true, inbox.StatusOpen, "new", "2", false, false, 1},
		},
		{
			"reopens a done request asked again",
			stored{inbox.Item{Link: link, Reason: "old", Ts: "1"}, inbox.StatusDone}, false,
			inbox.Item{Link: link, Reason: "new", Ts: "2"},
			want{true, inbox.StatusOpen, "new", "2", false, false, 1},
		},
		{
			"compares timestamps as numbers",
			stored{inbox.Item{Link: link, Ts: "9"}, inbox.StatusDone}, false,
			inbox.Item{Link: link, Ts: "10"},
			want{true, inbox.StatusOpen, "", "10", false, false, 1},
		},
		{
			"tells microseconds apart",
			stored{inbox.Item{Link: link, Ts: "1712345678.123456"}, inbox.StatusDone}, false,
			inbox.Item{Link: link, Ts: "1712345678.123457"},
			want{true, inbox.StatusOpen, "", "1712345678.123457", false, false, 1},
		},
		{
			"keeps a done request found again at the same timestamp",
			stored{inbox.Item{Link: link, Ts: "1"}, inbox.StatusDone}, false,
			inbox.Item{Link: link, Ts: "1.0"},
			want{false, inbox.StatusDone, "", "1", false, false, 1},
		},
		{
			"keeps a done request found again at an older timestamp",
			stored{inbox.Item{Link: link, Ts: "2"}, inbox.StatusDone}, false,
			inbox.Item{Link: link, Ts: "1"},
			want{false, inbox.StatusDone, "", "2", false, false, 1},
		},
		{
			"keeps a done request found again without a timestamp",
			stored{inbox.Item{Link: link, Ts: "1"}, inbox.StatusDone}, false,
			inbox.Item{Link: link},
			want{false, inbox.StatusDone, "", "1", false, false, 1},
		},
		{
			"refuses a timestamp that is not unix seconds",
			stored{inbox.Item{Link: other}, inbox.StatusOpen}, false,
			inbox.Item{Link: link, Ts: "yesterday"},
			want{false, "", "", "", false, true, 1},
		},
		{
			"refuses while paused",
			stored{inbox.Item{Link: other}, inbox.StatusOpen}, true,
			inbox.Item{Link: link},
			want{false, "", "", "", false, true, 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, tc.first.item, tc.first.status, now.Add(-time.Minute))
			require.NoError(t, s.SetPaused(tc.paused))

			it, added, err := add(s, tc.add, now)

			items, lerr := s.List()
			require.NoError(t, lerr)
			assert.Equal(t, tc.want, want{added, it.Status, it.Reason, it.Ts, it.Again, err != nil, len(items)})
		})
	}
}

// Every message of one thread is one request that keeps the link of the first one
func TestStoreAdd_Thread(t *testing.T) {
	t.Parallel()
	now := time.Now()
	first := inbox.Item{Thread: "C1/1", Link: link, Ts: "1", From: "U1", Author: "Ann", Summary: "first"}
	type want struct {
		id      string
		link    string
		from    string
		author  string
		summary string
		count   int
	}
	tcs := []struct {
		name string
		add  inbox.Item
		want want
	}{
		{
			"a newer message moves the author and the summary",
			inbox.Item{Thread: "C1/1", Link: other, Ts: "2", From: "U2", Author: "Bo", Summary: "second"},
			want{inbox.IdOf("C1/1"), link, "U2", "Bo", "second", 1},
		},
		{
			"an older message changes nothing",
			inbox.Item{Thread: "C1/1", Link: other, Ts: "0", From: "U2", Author: "Bo", Summary: "older"},
			want{inbox.IdOf("C1/1"), link, "U1", "Ann", "first", 1},
		},
		{
			"another thread is another request",
			inbox.Item{Thread: "C1/2", Link: other, Ts: "2", From: "U2", Author: "Bo", Summary: "second"},
			want{inbox.IdOf("C1/2"), other, "U2", "Bo", "second", 2},
		},
		{
			"a message without a thread is keyed by its link",
			inbox.Item{Link: other, Ts: "2", From: "U2", Author: "Bo", Summary: "second"},
			want{inbox.IdOf(other), other, "U2", "Bo", "second", 2},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, first, inbox.StatusOpen, now)

			it, _, err := add(s, tc.add, now)

			require.NoError(t, err)
			items, lerr := s.List()
			require.NoError(t, lerr)
			assert.Equal(t, tc.want, want{it.Id, it.Link, it.From, it.Author, it.Summary, len(items)})
		})
	}
}

// A scan older than the one that found the request again never reopens it once done
func TestStoreAdd_OlderScanAfterDone(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	_, _, err := add(s, inbox.Item{Link: link, Ts: "1"}, now)
	require.NoError(t, err)
	_, _, err = add(s, inbox.Item{Link: link, Ts: "2"}, now)
	require.NoError(t, err)
	_, err = s.Done(inbox.IdOf(link), "s1", now)
	require.NoError(t, err)

	_, added, err := add(s, inbox.Item{Link: link, Ts: "1"}, now)

	require.NoError(t, err)
	it, err := s.Get(inbox.IdOf(link))
	require.NoError(t, err)
	assert.Equal(t, []any{false, inbox.StatusDone}, []any{added, it.Status})
}

// A message that comes while the request is taken opens it again once the take settles
func TestStoreAdd_WhileTaken(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		status   inbox.Status
		reason   string
		followup bool
	}
	tcs := []struct {
		name   string
		settle func(s inbox.Store, id string) error
		want   want
	}{
		{
			"done opens it again as a follow-up",
			func(s inbox.Store, id string) error { _, err := s.Done(id, "s1", now); return err },
			want{inbox.StatusOpen, inbox.Replied, true},
		},
		{
			"a question opens it again since the reply may answer it",
			func(s inbox.Store, id string) error { _, err := s.Question(id, "s1", now); return err },
			want{inbox.StatusOpen, inbox.Replied, true},
		},
		{
			"holding it leaves it to the user",
			func(s inbox.Store, id string) error { _, err := s.Hold(id, "s1", time.Time{}, now); return err },
			want{inbox.StatusHeld, "", true},
		},
		{
			"the session ending gives it back",
			func(s inbox.Store, _ string) error { _, err := s.Release("s1", now); return err },
			want{inbox.StatusOpen, inbox.Released, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			id := seed(t, s, inbox.Item{Link: link, Ts: "1"}, inbox.StatusTaken, now).Id
			_, _, err := add(s, inbox.Item{Link: link, Ts: "2", Followup: true}, now)
			require.NoError(t, err)
			taken, err := s.Get(id)
			require.NoError(t, err)
			require.Equal(t, inbox.StatusTaken, taken.Status)

			require.NoError(t, tc.settle(s, id))

			it, err := s.Get(id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{it.Status, it.Reason, it.Followup})
			assert.False(t, it.Again)
		})
	}
}

func TestStoreGet(t *testing.T) {
	t.Parallel()
	type want struct {
		link     string
		status   inbox.Status
		notFound bool
		failed   bool
	}
	tcs := []struct {
		name string
		file string
		body string
		id   string
		want want
	}{
		{"reads an item", inbox.IdOf(link), `{"link":"` + link + `","status":"held"}`, inbox.IdOf(link),
			want{link, inbox.StatusHeld, false, false}},
		{"reads a legacy new item as open", inbox.IdOf(link), `{"link":"` + link + `","status":"new"}`, inbox.IdOf(link),
			want{link, inbox.StatusOpen, false, false}},
		{"reads a legacy ask item as open", inbox.IdOf(link), `{"link":"` + link + `","status":"ask"}`, inbox.IdOf(link),
			want{link, inbox.StatusOpen, false, false}},
		{"refuses a corrupt item", inbox.IdOf(link), `{"link":`, inbox.IdOf(link), want{"", "", false, true}},
		{"an unknown id is not found", inbox.IdOf(link), `{}`, inbox.IdOf(other), want{"", "", true, true}},
		{"a path like id is not found", inbox.IdOf(link), `{}`, "../dest", want{"", "", true, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "inbox", tc.file+".json"), []byte(tc.body), 0o644))

			it, err := inbox.New(dir).Get(tc.id)

			assert.Equal(t, tc.want, want{it.Link, it.Status, errors.Is(err, inbox.ErrNotFound), err != nil})
		})
	}
}

func TestItemOpen(t *testing.T) {
	t.Parallel()
	now := time.Now()
	day := 24 * time.Hour
	item := func(status inbox.Status, age time.Duration) inbox.Item {
		return inbox.Item{Status: status, UpdatedAt: now.Add(-age)}
	}
	tcs := []struct {
		name string
		item inbox.Item
		want bool
	}{
		{"an open request", item(inbox.StatusOpen, 0), true},
		{"a held request without a time waits for the user", item(inbox.StatusHeld, 40*day), false},
		{"a held request before its time", inbox.Item{Status: inbox.StatusHeld, HeldUntil: now.Add(time.Minute)}, false},
		{"a held request whose time came", inbox.Item{Status: inbox.StatusHeld, HeldUntil: now}, true},
		{"a fresh question", item(inbox.StatusQuestion, 2*day), false},
		{"a question left unanswered", item(inbox.StatusQuestion, 3*day), true},
		{"a fresh take", item(inbox.StatusTaken, 23*time.Hour), false},
		{"a take kept past a day", item(inbox.StatusTaken, day), true},
		{"a done request", item(inbox.StatusDone, 40*day), false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.item.Open(now))
		})
	}
}

func TestStoreTake(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type take struct {
		session string
		after   time.Duration
	}
	type step struct {
		session string
		failed  bool
	}
	tcs := []struct {
		name   string
		status inbox.Status
		id     string
		takes  []take
		want   []step
	}{
		{"one session takes an open request", inbox.StatusOpen, inbox.IdOf(link), []take{{"s2", 0}}, []step{{"s2", false}}},
		{
			"the same session takes again and another is refused", inbox.StatusOpen, inbox.IdOf(link),
			[]take{{"s2", 0}, {"s2", 0}, {"s3", 23 * time.Hour}}, []step{{"s2", false}, {"s2", false}, {"", true}},
		},
		{
			"another session takes over a take left a day", inbox.StatusOpen, inbox.IdOf(link),
			[]take{{"s2", 0}, {"s3", 25 * time.Hour}, {"s2", 25 * time.Hour}}, []step{{"s2", false}, {"s3", false}, {"", true}},
		},
		{"a held request is taken by its id", inbox.StatusHeld, inbox.IdOf(link), []take{{"s2", 0}}, []step{{"s2", false}}},
		{"a question is taken by its id", inbox.StatusQuestion, inbox.IdOf(link), []take{{"s2", 0}}, []step{{"s2", false}}},
		{"a done request is refused", inbox.StatusDone, inbox.IdOf(link), []take{{"s2", 0}}, []step{{"", true}}},
		{"a done request stays done long after",
			inbox.StatusDone, inbox.IdOf(link), []take{{"s2", 25 * time.Hour}}, []step{{"", true}}},
		{"an unknown id is refused", inbox.StatusOpen, inbox.IdOf(other), []take{{"s2", 0}}, []step{{"", true}}},
		{"a path like id is refused", inbox.StatusOpen, "../dest", []take{{"s2", 0}}, []step{{"", true}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: link}, tc.status, now)

			got := make([]step, 0, len(tc.takes))
			for _, tk := range tc.takes {
				it, err := s.Take(tc.id, tk.session, now.Add(tk.after))
				got = append(got, step{it.SessionId, err != nil})
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStoreTake_Paused(t *testing.T) {
	t.Parallel()
	s := inbox.New(t.TempDir())
	_, _, err := add(s, inbox.Item{Link: link}, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.SetPaused(true))

	_, err = s.Take(inbox.IdOf(link), "s1", time.Now())

	assert.ErrorIs(t, err, inbox.ErrPaused)
}

// Sessions race for one request while the winner settles it at once
func TestStoreTake_Race(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const sessions = 8
	for i := range 50 {
		s := inbox.New(t.TempDir())
		l := fmt.Sprintf("https://w.slack.com/archives/C1/p%d", i)
		it, _, err := add(s, inbox.Item{Link: l}, now)
		require.NoError(t, err)
		var wg sync.WaitGroup
		var mu sync.Mutex
		took := 0
		for n := range sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.Take(it.Id, strconv.Itoa(n), now); err != nil {
					return
				}
				mu.Lock()
				took++
				mu.Unlock()
				_, _ = s.Done(it.Id, strconv.Itoa(n), now)
			}()
		}
		wg.Wait()
		got, err := s.Get(it.Id)
		require.NoError(t, err)
		assert.Equal(t, []any{1, inbox.StatusDone}, []any{took, got.Status}, "run %d", i)
	}
}

func TestStoreTakenBy(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		link  string
		found bool
	}
	tcs := []struct {
		name    string
		session string
		after   time.Duration
		done    bool
		want    want
	}{
		{"finds the request the session took", "s1", 0, false, want{link, true}},
		{"finds nothing for another session", "s2", 0, false, want{"", false}},
		{"finds nothing once the take is a day old", "s1", 25 * time.Hour, false, want{"", false}},
		{"finds nothing once the request is done", "s1", 0, true, want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: other}, inbox.StatusOpen, now)
			status := inbox.StatusTaken
			if tc.done {
				status = inbox.StatusDone
			}
			seed(t, s, inbox.Item{Link: link}, status, now)

			it, found, err := s.TakenBy(tc.session, now.Add(tc.after))

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{it.Link, found})
		})
	}
}

type settled struct {
	status inbox.Status
	reason string
	failed bool
}

func TestStoreHold(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name    string
		status  inbox.Status
		session string
		want    settled
	}{
		{"a taken request is put off", inbox.StatusTaken, "s1", settled{inbox.StatusHeld, "", false}},
		{"an open request is put off", inbox.StatusOpen, "s2", settled{inbox.StatusHeld, "", false}},
		{"a question is put off", inbox.StatusQuestion, "s2", settled{inbox.StatusHeld, "", false}},
		{"a held request is put off again", inbox.StatusHeld, "s2", settled{inbox.StatusHeld, "", false}},
		{"a request another session took is refused", inbox.StatusTaken, "s2", settled{inbox.StatusTaken, "", true}},
		{"a done request is refused", inbox.StatusDone, "s1", settled{inbox.StatusDone, "", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: link}, tc.status, now)

			_, err := s.Hold(inbox.IdOf(link), tc.session, time.Time{}, now)

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, settled{it.Status, it.Reason, err != nil})
		})
	}
}

// Only a take of the session waits for the requester
func TestStoreQuestion_From(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name    string
		status  inbox.Status
		session string
		want    settled
	}{
		{"a taken request waits for the requester", inbox.StatusTaken, "s1", settled{inbox.StatusQuestion, "", false}},
		{"a request another session took is refused", inbox.StatusTaken, "s2", settled{inbox.StatusTaken, "", true}},
		{"an open request is refused", inbox.StatusOpen, "s1", settled{inbox.StatusOpen, "", true}},
		{"a held request is refused", inbox.StatusHeld, "s1", settled{inbox.StatusHeld, "", true}},
		{"a done request is refused", inbox.StatusDone, "s1", settled{inbox.StatusDone, "", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: link}, tc.status, now)

			_, err := s.Question(inbox.IdOf(link), tc.session, now)

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, settled{it.Status, it.Reason, err != nil})
		})
	}
}

// Only the session holding a take settles it while the take keeps that session busy
func TestStoreDone_Session(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		status  inbox.Status
		session string
		taken   bool
	}
	tcs := []struct {
		name     string
		takes    []string
		takeover time.Duration
		settler  string
		after    time.Duration
		want     want
	}{
		{"the holder settles its take", []string{"s1"}, 0, "s1", 0, want{inbox.StatusDone, "", false}},
		{"another session cannot settle an active take", []string{"s1"}, 0, "s2", 0, want{inbox.StatusTaken, "s1", true}},
		{
			"the old session cannot settle after a takeover", []string{"s1", "s2"}, 25 * time.Hour, "s1", 25 * time.Hour,
			want{inbox.StatusTaken, "s2", true},
		},
		{
			"the new holder settles after a takeover", []string{"s1", "s2"}, 25 * time.Hour, "s2", 25 * time.Hour,
			want{inbox.StatusDone, "", false},
		},
		{"any session settles a take left a day", []string{"s1"}, 0, "s2", 25 * time.Hour, want{inbox.StatusDone, "", false}},
		{"any session settles a request no one took", nil, 0, "s2", 0, want{inbox.StatusDone, "", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := add(s, inbox.Item{Link: link}, now)
			require.NoError(t, err)
			for i, session := range tc.takes {
				_, err := s.Take(inbox.IdOf(link), session, now.Add(time.Duration(i)*tc.takeover))
				require.NoError(t, err)
			}

			_, err = s.Done(inbox.IdOf(link), tc.settler, now.Add(tc.after))

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, want{it.Status, it.SessionId, errors.Is(err, inbox.ErrTaken)})
		})
	}
}

func TestStoreDoneById(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name    string
		status  inbox.Status
		id      string
		session string
		want    settled
	}{
		{"a taken request is done", inbox.StatusTaken, inbox.IdOf(link), "s1", settled{inbox.StatusDone, "", false}},
		{"an open request is done", inbox.StatusOpen, inbox.IdOf(link), "s2", settled{inbox.StatusDone, "", false}},
		{"a held request is done", inbox.StatusHeld, inbox.IdOf(link), "s1", settled{inbox.StatusDone, "", false}},
		{"a question is done", inbox.StatusQuestion, inbox.IdOf(link), "s1", settled{inbox.StatusDone, "", false}},
		{"closing twice is fine", inbox.StatusDone, inbox.IdOf(link), "s1", settled{inbox.StatusDone, "", false}},
		{"a request another session took is refused",
			inbox.StatusTaken, inbox.IdOf(link), "s2", settled{inbox.StatusTaken, "", true}},
		{"an id that never came through the inbox is fine",
			inbox.StatusOpen, inbox.IdOf(other), "s1", settled{inbox.StatusOpen, "", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: link}, tc.status, now)

			err := s.DoneById(tc.id, tc.session, now)

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, settled{it.Status, it.Reason, err != nil})
		})
	}
}

func TestStoreByOrigin(t *testing.T) {
	t.Parallel()
	s := inbox.New(t.TempDir())
	seed(t, s, inbox.Item{Thread: "C1/1", Link: link}, inbox.StatusOpen, time.Now())
	seed(t, s, inbox.Item{Link: "https://github.com/o/r/pull/1"}, inbox.StatusOpen, time.Now())
	type want struct {
		id       string
		notFound bool
	}
	tcs := []struct {
		name   string
		origin string
		want   want
	}{
		{"the link of the first message", link, want{inbox.IdOf("C1/1"), false}},
		{"the thread key", "C1/1", want{inbox.IdOf("C1/1"), false}},
		{"a request without a thread",
			"https://github.com/o/r/pull/1", want{inbox.IdOf("https://github.com/o/r/pull/1"), false}},
		{"a link opened by hand is not found", other, want{"", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			it, err := s.ByOrigin(tc.origin)
			assert.Equal(t, tc.want, want{it.Id, errors.Is(err, inbox.ErrNotFound)})
		})
	}
}

// A session that ended gives its takes back at once
func TestStoreRelease(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		count    int
		statuses []string
	}
	tcs := []struct {
		name    string
		session string
		want    want
	}{
		{
			"the session's takes open with a reason", "s1",
			want{2, []string{"p1 open " + inbox.Released, "p2 open " + inbox.Released, "p3 taken s2", "p4 held", "p5 question"}},
		},
		{
			"a session with no take changes nothing", "s9",
			want{0, []string{"p1 taken s1", "p2 taken s1", "p3 taken s2", "p4 held", "p5 question"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			seed(t, s, inbox.Item{Link: "p1"}, inbox.StatusTaken, now.Add(-5*time.Minute))
			seed(t, s, inbox.Item{Link: "p2"}, inbox.StatusTaken, now.Add(-4*time.Minute))
			_, err := s.Take(seed(t, s, inbox.Item{Link: "p3"}, inbox.StatusOpen, now.Add(-3*time.Minute)).Id, "s2", now)
			require.NoError(t, err)
			seed(t, s, inbox.Item{Link: "p4"}, inbox.StatusHeld, now.Add(-2*time.Minute))
			seed(t, s, inbox.Item{Link: "p5"}, inbox.StatusQuestion, now.Add(-time.Minute))

			n, err := s.Release(tc.session, now)

			require.NoError(t, err)
			items, err := s.List()
			require.NoError(t, err)
			got := []string{}
			for i := len(items) - 1; i >= 0; i-- {
				it := items[i]
				row := strings.Join([]string{it.Link, string(it.Status), it.SessionId + it.Reason}, " ")
				got = append(got, strings.TrimSpace(row))
			}
			assert.Equal(t, tc.want, want{n, got})
		})
	}
}

func TestStoreWaiting(t *testing.T) {
	t.Parallel()
	now := time.Now()
	stored := []struct {
		link   string
		status inbox.Status
		at     time.Duration
	}{
		{"p0", inbox.StatusQuestion, -4 * 24 * time.Hour},
		{"p1", inbox.StatusTaken, -25 * time.Hour},
		{"p2", inbox.StatusOpen, -2 * time.Hour},
		{"p3", inbox.StatusDone, -90 * time.Minute},
		{"p4", inbox.StatusTaken, -15 * time.Minute},
		{"p5", inbox.StatusHeld, -10 * time.Minute},
	}
	tcs := []struct {
		name  string
		after time.Duration
		want  []string
	}{
		{
			"oldest first with a take left a day and an unanswered question as open", 0,
			[]string{"p0 open " + inbox.Unanswered, "p1 open " + inbox.Released, "p2 open ", "p5 held "},
		},
		{
			"a take ages into open", 24 * time.Hour,
			[]string{
				"p0 open " + inbox.Unanswered, "p1 open " + inbox.Released, "p2 open ",
				"p4 open " + inbox.Released, "p5 held ",
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, st := range stored {
				seed(t, s, inbox.Item{Link: st.link}, st.status, now.Add(st.at))
			}

			all, err := s.List()

			require.NoError(t, err)
			items := inbox.Waiting(all, now.Add(tc.after))
			got := make([]string, 0, len(items))
			for _, it := range items {
				got = append(got, it.Link+" "+string(it.Status)+" "+it.Reason)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStoreList_Removed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	dir := t.TempDir()
	s := inbox.New(dir)
	for i := range 200 {
		_, _, err := add(s, inbox.Item{Link: fmt.Sprintf("x%d", i)}, now)
		require.NoError(t, err)
	}
	items, err := s.List()
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, it := range items {
			_ = os.Remove(filepath.Join(dir, "inbox", it.Id+".json"))
		}
	}()
	failed := 0
	for range 50 {
		if _, err := s.List(); err != nil {
			failed++
		}
	}
	<-done

	assert.Zero(t, failed)
}

func TestStoreCursor(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	type want struct {
		cursor string
		failed bool
		files  []string
	}
	tcs := []struct {
		name    string
		key     string
		advance []string
		want    want
	}{
		{"starts at now before any message", "mention", nil, want{"1800000000", false, []string{"cursor"}}},
		{
			"moves to the newest timestamp", "mention", []string{"1800000100.000100", "1800000500.000200"},
			want{"1800000500.000200", false, []string{"cursor"}},
		},
		{
			"never moves back", "mention", []string{"1800000500.000200", "1800000100.000100"},
			want{"1800000500.000200", false, []string{"cursor"}},
		},
		{"rejects a timestamp that is not a number", "mention", []string{"yesterday"}, want{"1800000000", true, []string{"cursor"}}},
		{"rejects infinity", "mention", []string{"inf", "1800000100"}, want{"1800000100", true, []string{"cursor"}}},
		{"rejects not a number", "mention", []string{"NaN", "1800000100"}, want{"1800000100", true, []string{"cursor"}}},
		{"rejects an exponent", "mention", []string{"1e10"}, want{"1800000000", true, []string{"cursor"}}},
		{"keeps a rule key in its own file", "alerts", []string{"1800000100"}, want{"1800000100", false, []string{"cursor-alerts"}}},
		{"rejects a key that is not lowercase", "Alerts", nil, want{"", true, nil}},
		{"rejects a path like key", "../x", []string{"1800000100"}, want{"", true, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := inbox.New(dir)
			failed := false
			for _, ts := range tc.advance {
				err := s.Advance(tc.key, ts)
				failed = failed || err != nil
			}

			got, err := s.Cursor(tc.key, now)
			again, aerr := s.Cursor(tc.key, now.Add(time.Hour))

			assert.Equal(t, got, again, "the start is kept")
			assert.Equal(t, err, aerr)
			assert.Equal(t, tc.want, want{got, failed || err != nil, cursorFiles(t, dir)})
		})
	}
}

func cursorFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "inbox"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cursor") {
			out = append(out, e.Name())
		}
	}
	return out
}

// A reader never sees an empty or older cursor while sessions advance it
func TestStoreCursor_Race(t *testing.T) {
	t.Parallel()
	s := inbox.New(t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	_, err := s.Cursor("mention", now)
	require.NoError(t, err)
	const writers, steps = 4, 300
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range steps {
				// Writers interleave so each one also sends timestamps older than the stored one
				_ = s.Advance("mention", strconv.Itoa(1_800_000_001+i*writers+(writers-1-w)))
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	var bad []string
	last := 0.0
	for running := true; running; {
		select {
		case <-done:
			running = false
		default:
		}
		c, err := s.Cursor("mention", now)
		cur, perr := strconv.ParseFloat(c, 64)
		if err != nil || perr != nil || cur < last {
			bad = append(bad, fmt.Sprintf("%q %v", c, err))
		}
		last = max(last, cur)
	}
	got, err := s.Cursor("mention", now)
	require.NoError(t, err)

	assert.Empty(t, bad)
	assert.Equal(t, strconv.Itoa(1_800_000_000+steps*writers), got)
}

func TestStoreCursor_Paused(t *testing.T) {
	t.Parallel()
	s := inbox.New(t.TempDir())
	require.NoError(t, s.SetPaused(true))

	_, err := s.Cursor("mention", time.Now())

	assert.ErrorIs(t, err, inbox.ErrPaused)
}
