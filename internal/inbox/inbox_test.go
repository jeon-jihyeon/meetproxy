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

func TestStoreAdd(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		added  bool
		status inbox.Status
		reason string
		ts     string
		failed bool
		count  int
	}
	tcs := []struct {
		name   string
		first  inbox.Item
		paused bool
		add    inbox.Item
		want   want
	}{
		{
			"stores a new link",
			inbox.Item{Link: other, Status: inbox.StatusNew}, false,
			inbox.Item{Link: link, Status: inbox.StatusNew},
			want{true, inbox.StatusNew, "", "", false, 2},
		},
		{
			"keeps a link seen before",
			inbox.Item{Link: link, Status: inbox.StatusAsk, Reason: "a"}, false,
			inbox.Item{Link: link, Status: inbox.StatusNew, Reason: "b"},
			want{false, inbox.StatusAsk, "a", "", false, 1},
		},
		{
			"keeps a waiting request asked again with its newest timestamp",
			inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "1"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "2"},
			want{false, inbox.StatusNew, "", "2", false, 1},
		},
		{
			"keeps the timestamp of a waiting request found by an older scan",
			inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "2"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "1"},
			want{false, inbox.StatusNew, "", "2", false, 1},
		},
		{
			"reopens a done request asked again",
			inbox.Item{Link: link, Status: inbox.StatusDone, Reason: "old", Ts: "1"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Reason: "new", Ts: "2"},
			want{true, inbox.StatusAsk, "new", "2", false, 1},
		},
		{
			"compares timestamps as numbers",
			inbox.Item{Link: link, Status: inbox.StatusDone, Ts: "9"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "10"},
			want{true, inbox.StatusAsk, "", "10", false, 1},
		},
		{
			"tells microseconds apart",
			inbox.Item{Link: link, Status: inbox.StatusDone, Ts: "1712345678.123456"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "1712345678.123457"},
			want{true, inbox.StatusAsk, "", "1712345678.123457", false, 1},
		},
		{
			"keeps a done request found again at the same timestamp",
			inbox.Item{Link: link, Status: inbox.StatusDone, Ts: "1"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "1.0"},
			want{false, inbox.StatusDone, "", "1", false, 1},
		},
		{
			"keeps a done request found again at an older timestamp",
			inbox.Item{Link: link, Status: inbox.StatusDone, Ts: "2"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk, Ts: "1"},
			want{false, inbox.StatusDone, "", "2", false, 1},
		},
		{
			"keeps a done request found again without a timestamp",
			inbox.Item{Link: link, Status: inbox.StatusDone, Ts: "1"}, false,
			inbox.Item{Link: link, Status: inbox.StatusAsk},
			want{false, inbox.StatusDone, "", "1", false, 1},
		},
		{
			"refuses a timestamp that is not unix seconds",
			inbox.Item{Link: other, Status: inbox.StatusNew}, false,
			inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "yesterday"},
			want{false, "", "", "", true, 1},
		},
		{
			"refuses while paused",
			inbox.Item{Link: other, Status: inbox.StatusNew}, true,
			inbox.Item{Link: link, Status: inbox.StatusNew},
			want{false, "", "", "", true, 1},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(tc.first, now.Add(-time.Minute))
			require.NoError(t, err)
			require.NoError(t, s.SetPaused(tc.paused))

			it, added, err := s.Add(tc.add, now)

			items, lerr := s.List()
			require.NoError(t, lerr)
			assert.Equal(t, tc.want, want{added, it.Status, it.Reason, it.Ts, err != nil, len(items)})
		})
	}
}

// A scan older than the one that found the request again never reopens it once done
func TestStoreAdd_OlderScanAfterDone(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "1"}, now)
	require.NoError(t, err)
	_, _, err = s.Add(inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "2"}, now)
	require.NoError(t, err)
	_, err = s.Done(inbox.IdOf(link), "s1", now)
	require.NoError(t, err)

	_, added, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew, Ts: "1"}, now)

	require.NoError(t, err)
	it, err := s.Get(inbox.IdOf(link))
	require.NoError(t, err)
	assert.Equal(t, []any{false, inbox.StatusDone}, []any{added, it.Status})
}

func TestItemAt(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		item inbox.Item
		root string
		want bool
	}{
		{"matches the root", inbox.Item{Place: "/a/svc", Name: "svc"}, "/a/svc", true},
		{"a place of the same name elsewhere does not match", inbox.Item{Place: "/b/svc", Name: "svc"}, "/a/svc", false},
		{"a request without a root matches by name", inbox.Item{Name: "svc"}, "/a/svc", true},
		{"a request without a place matches nothing", inbox.Item{}, "/a/svc", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.item.At(tc.root, "svc"))
		})
	}
}

func TestStoreGet(t *testing.T) {
	t.Parallel()
	type want struct {
		link     string
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
		{"reads an item", inbox.IdOf(link), `{"link":"` + link + `"}`, inbox.IdOf(link), want{link, false, false}},
		{"refuses a corrupt item", inbox.IdOf(link), `{"link":`, inbox.IdOf(link), want{"", false, true}},
		{"an unknown id is not found", inbox.IdOf(link), `{}`, inbox.IdOf(other), want{"", true, true}},
		{"a path like id is not found", inbox.IdOf(link), `{}`, "../dest", want{"", true, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "inbox", tc.file+".json"), []byte(tc.body), 0o644))

			it, err := inbox.New(dir).Get(tc.id)

			assert.Equal(t, tc.want, want{it.Link, errors.Is(err, inbox.ErrNotFound), err != nil})
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
		{"one session takes a new request", inbox.StatusNew, inbox.IdOf(link), []take{{"s1", 0}}, []step{{"s1", false}}},
		{
			"the same session takes again and another is refused", inbox.StatusAsk, inbox.IdOf(link),
			[]take{{"s1", 0}, {"s1", 0}, {"s2", 0}}, []step{{"s1", false}, {"s1", false}, {"", true}},
		},
		{
			"another session takes over a take left for an hour", inbox.StatusNew, inbox.IdOf(link),
			[]take{{"s1", 0}, {"s2", 2 * time.Hour}, {"s1", 2 * time.Hour}}, []step{{"s1", false}, {"s2", false}, {"", true}},
		},
		{"a held request is taken by its id", inbox.StatusHeld, inbox.IdOf(link), []take{{"s1", 0}}, []step{{"s1", false}}},
		{"a done request is refused", inbox.StatusDone, inbox.IdOf(link), []take{{"s1", 0}}, []step{{"", true}}},
		{"a done request stays done long after", inbox.StatusDone, inbox.IdOf(link), []take{{"s1", 2 * time.Hour}}, []step{{"", true}}},
		{"an unknown id is refused", inbox.StatusNew, inbox.IdOf(other), []take{{"s1", 0}}, []step{{"", true}}},
		{"a path like id is refused", inbox.StatusNew, "../dest", []take{{"s1", 0}}, []step{{"", true}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status}, now)
			require.NoError(t, err)

			got := make([]step, 0, len(tc.takes))
			for _, tk := range tc.takes {
				it, err := s.Take(tc.id, tk.session, now.Add(tk.after))
				got = append(got, step{it.SessionId, err != nil})
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// Sessions race for one request while the winner settles it at once
func TestStoreTake_Race(t *testing.T) {
	t.Parallel()
	now := time.Now()
	const sessions = 8
	for i := range 50 {
		s := inbox.New(t.TempDir())
		l := fmt.Sprintf("https://w.slack.com/archives/C1/p%d", i)
		it, _, err := s.Add(inbox.Item{Link: l, Status: inbox.StatusNew}, now)
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

func TestStoreClaim(t *testing.T) {
	t.Parallel()
	now := time.Now()
	type want struct {
		ok     bool
		failed bool
		status inbox.Status
	}
	tcs := []struct {
		name    string
		link    string
		session string
		after   time.Duration
		want    want
	}{
		{"a session takes one request at a time", other, "s1", 0, want{false, false, inbox.StatusNew}},
		{"a request taken long ago no longer keeps the session busy", other, "s1", 2 * time.Hour, want{true, false, inbox.StatusTaken}},
		{"another session takes a request of its own", other, "s2", 0, want{true, false, inbox.StatusTaken}},
		{"a request another session works on is refused", link, "s2", 0, want{false, true, inbox.StatusTaken}},
		{"a request left taken for an hour is taken over", link, "s2", 2 * time.Hour, want{true, false, inbox.StatusTaken}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, l := range []string{link, other} {
				_, _, err := s.Add(inbox.Item{Link: l, Status: inbox.StatusNew}, now)
				require.NoError(t, err)
			}
			_, ok, err := s.Claim(inbox.IdOf(link), "s1", now)
			require.NoError(t, err)
			require.True(t, ok)

			_, ok, err = s.Claim(inbox.IdOf(tc.link), tc.session, now.Add(tc.after))

			it, gerr := s.Get(inbox.IdOf(tc.link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, want{ok, err != nil, it.Status})
		})
	}
}

// A held request waits for the user so a session never claims it on its own
func TestStoreClaim_Held(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusHeld}, now)
	require.NoError(t, err)

	_, ok, err := s.Claim(inbox.IdOf(link), "s1", now)

	it, gerr := s.Get(inbox.IdOf(link))
	require.NoError(t, gerr)
	assert.Equal(t, []any{false, true, inbox.StatusHeld}, []any{ok, err != nil, it.Status})
}

func TestStoreClaim_Paused(t *testing.T) {
	t.Parallel()
	s := inbox.New(t.TempDir())
	_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, time.Now())
	require.NoError(t, err)
	require.NoError(t, s.SetPaused(true))

	_, _, err = s.Claim(inbox.IdOf(link), "s1", time.Now())

	assert.ErrorIs(t, err, inbox.ErrPaused)
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
		{"finds nothing once the take is an hour old", "s1", 2 * time.Hour, false, want{"", false}},
		{"finds nothing once the request is done", "s1", 0, true, want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, l := range []string{link, other} {
				_, _, err := s.Add(inbox.Item{Link: l, Status: inbox.StatusNew}, now)
				require.NoError(t, err)
			}
			_, err := s.Take(inbox.IdOf(link), "s1", now)
			require.NoError(t, err)
			if tc.done {
				_, err = s.Done(inbox.IdOf(link), "s1", now)
				require.NoError(t, err)
			}

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

func TestStoreAsk(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name    string
		status  inbox.Status
		session string
		reason  string
		want    settled
	}{
		{"a taken request asks the user", inbox.StatusTaken, "s1", "needs a decision", settled{inbox.StatusAsk, "needs a decision", false}},
		{"a new request asks the user", inbox.StatusNew, "s2", "needs a decision", settled{inbox.StatusAsk, "needs a decision", false}},
		{"an empty reason keeps the stored one", inbox.StatusTaken, "s1", "", settled{inbox.StatusAsk, "stored", false}},
		{"a request another session took is refused", inbox.StatusTaken, "s2", "late", settled{inbox.StatusTaken, "stored", true}},
		{"a held request is refused", inbox.StatusHeld, "s1", "late", settled{inbox.StatusHeld, "stored", true}},
		{"a done request is refused", inbox.StatusDone, "s1", "late", settled{inbox.StatusDone, "stored", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status, Reason: "stored", SessionId: "s1"}, now)
			require.NoError(t, err)

			_, err = s.Ask(inbox.IdOf(link), tc.session, tc.reason, now)

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, settled{it.Status, it.Reason, err != nil})
		})
	}
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
		{"a request for the user is put off", inbox.StatusAsk, "s2", settled{inbox.StatusHeld, "", false}},
		{"a request another session took is refused", inbox.StatusTaken, "s2", settled{inbox.StatusTaken, "", true}},
		{"a new request is refused", inbox.StatusNew, "s1", settled{inbox.StatusNew, "", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status, SessionId: "s1"}, now)
			require.NoError(t, err)

			_, err = s.Hold(inbox.IdOf(link), tc.session, now)

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
		{"the old session cannot settle after a takeover", []string{"s1", "s2"}, 2 * time.Hour, "s1", 2 * time.Hour, want{inbox.StatusTaken, "s2", true}},
		{"the new holder settles after a takeover", []string{"s1", "s2"}, 2 * time.Hour, "s2", 2 * time.Hour, want{inbox.StatusDone, "", false}},
		{"any session settles a take left for an hour", []string{"s1"}, 0, "s2", 2 * time.Hour, want{inbox.StatusDone, "", false}},
		{"any session settles a request no one took", nil, 0, "s2", 0, want{inbox.StatusDone, "", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, now)
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

func TestStoreDoneByLink(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tcs := []struct {
		name   string
		status inbox.Status
		link   string
		want   settled
	}{
		{"a taken request is done", inbox.StatusTaken, link, settled{inbox.StatusDone, "", false}},
		{"a held request is done", inbox.StatusHeld, link, settled{inbox.StatusDone, "", false}},
		{"closing twice is fine", inbox.StatusDone, link, settled{inbox.StatusDone, "", false}},
		{"a link that never came through the inbox is fine", inbox.StatusNew, "https://github.com/o/r/pull/1", settled{inbox.StatusNew, "", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			_, _, err := s.Add(inbox.Item{Link: link, Status: tc.status, SessionId: "s1"}, now)
			require.NoError(t, err)

			err = s.DoneByLink(tc.link, "s1", now)

			it, gerr := s.Get(inbox.IdOf(link))
			require.NoError(t, gerr)
			assert.Equal(t, tc.want, settled{it.Status, it.Reason, err != nil})
		})
	}
}

func TestStoreWaiting(t *testing.T) {
	t.Parallel()
	now := time.Now()
	stored := []struct {
		item inbox.Item
		at   time.Duration
	}{
		{inbox.Item{Link: "p1", Status: inbox.StatusTaken, SessionId: "s1"}, -3 * time.Hour},
		{inbox.Item{Link: "p2", Status: inbox.StatusNew}, -2 * time.Hour},
		{inbox.Item{Link: "p3", Status: inbox.StatusDone}, -90 * time.Minute},
		{inbox.Item{Link: "p4", Status: inbox.StatusTaken, SessionId: "s2"}, -15 * time.Minute},
		{inbox.Item{Link: "p5", Status: inbox.StatusHeld}, -10 * time.Minute},
	}
	tcs := []struct {
		name  string
		after time.Duration
		want  []string
	}{
		{"oldest first with a take left for an hour as ask", 0, []string{"p1 ask", "p2 new", "p5 held"}},
		{"a take ages into ask", time.Hour, []string{"p1 ask", "p2 new", "p4 ask", "p5 held"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := inbox.New(t.TempDir())
			for _, st := range stored {
				_, _, err := s.Add(st.item, now.Add(st.at))
				require.NoError(t, err)
			}

			items, err := s.Waiting(now.Add(tc.after))

			require.NoError(t, err)
			got := make([]string, 0, len(items))
			for _, it := range items {
				got = append(got, it.Link+" "+string(it.Status))
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// Another session prunes or settles while the list is read
func TestStoreList_Removed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	dir := t.TempDir()
	s := inbox.New(dir)
	for i := range 200 {
		_, _, err := s.Add(inbox.Item{Link: fmt.Sprintf("x%d", i), Status: inbox.StatusNew}, now)
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

func TestStoreAdd_PrunesOldDone(t *testing.T) {
	t.Parallel()
	now := time.Now()
	s := inbox.New(t.TempDir())
	_, _, err := s.Add(inbox.Item{Link: link, Status: inbox.StatusNew}, now.Add(-10*24*time.Hour))
	require.NoError(t, err)
	_, err = s.Done(inbox.IdOf(link), "s1", now.Add(-8*24*time.Hour))
	require.NoError(t, err)
	_, _, err = s.Add(inbox.Item{Link: "https://w.slack.com/archives/C1/p9", Status: inbox.StatusNew}, now.Add(-24*time.Hour))
	require.NoError(t, err)
	_, err = s.Done(inbox.IdOf("https://w.slack.com/archives/C1/p9"), "s1", now.Add(-24*time.Hour))
	require.NoError(t, err)

	_, _, err = s.Add(inbox.Item{Link: other, Status: inbox.StatusNew}, now)
	require.NoError(t, err)

	items, err := s.List()
	require.NoError(t, err)
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Id)
	}
	assert.ElementsMatch(t, []string{inbox.IdOf(other), inbox.IdOf("https://w.slack.com/archives/C1/p9")}, ids)
}
