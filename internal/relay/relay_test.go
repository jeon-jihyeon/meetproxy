package relay_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

func TestStoreOpen(t *testing.T) {
	t.Parallel()
	type args struct {
		first        string
		firstTarget  string
		second       string
		secondTarget string
	}
	type want struct {
		sameId  bool
		origin  string
		target  string
		saved   string
		observe []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"continues the same origin and keeps observations", args{"o1", "", "o1", ""}, want{true, "o1", "", "", []string{"a.go"}}},
		{"takes a target given to the same origin", args{"o1", "", "o1", "pr"}, want{true, "o1", "pr", "pr", []string{"a.go"}}},
		{"replaces the target of the same origin", args{"o1", "pr1", "o1", "pr2"}, want{true, "o1", "pr2", "pr2", []string{"a.go"}}},
		{"keeps the target when none is given", args{"o1", "pr", "o1", ""}, want{true, "o1", "pr", "pr", []string{"a.go"}}},
		{"opens a new relay for another origin", args{"o1", "pr", "o2", ""}, want{false, "o2", "", "", []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			now := time.Now()
			first, err := s.Open("s1", tc.args.first, tc.args.firstTarget, now)
			require.NoError(t, err)
			require.NoError(t, s.Observe("s1", locmap.Path{Root: "/r", Rel: "a.go"}))

			second, err := s.Open("s1", tc.args.second, tc.args.secondTarget, now)
			require.NoError(t, err)
			cur, err := s.Current("s1")
			require.NoError(t, err)
			ps, err := s.Observed(second.Id)
			require.NoError(t, err)
			got := []string{}
			for _, p := range ps {
				got = append(got, p.Rel)
			}
			assert.Equal(t, tc.want, want{first.Id == second.Id, second.Origin, second.Target, cur.Target, got})
		})
	}
}

func TestStoreObserve(t *testing.T) {
	t.Parallel()
	a := locmap.Path{Name: "svc", Root: "/r", Rel: "a.go"}
	b := locmap.Path{Name: "svc", Root: "/r", Rel: "b.go"}

	type args struct {
		open    bool
		observe []locmap.Path
	}
	type want struct {
		observed []string
		files    int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"records in order without duplicates", args{true, []locmap.Path{a, b, a}}, want{[]string{"a.go", "b.go"}, 1}},
		{"writes nothing without an open relay", args{false, []locmap.Path{a}}, want{[]string{}, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			s := relay.New(dir)
			var id string
			if tc.args.open {
				r, err := s.Open("s1", "origin", "", time.Now())
				require.NoError(t, err)
				id = r.Id
			}
			for _, p := range tc.args.observe {
				require.NoError(t, s.Observe("s1", p))
			}
			ps, err := s.Observed(id)
			require.NoError(t, err)
			got := []string{}
			for _, p := range ps {
				got = append(got, p.Rel)
			}
			files, _ := os.ReadDir(filepath.Join(dir, "relay", "observed"))
			assert.Equal(t, tc.want, want{got, len(files)})
		})
	}
}

func TestStoreClose(t *testing.T) {
	t.Parallel()
	type want struct {
		closeErr   error
		currentErr error
	}
	tcs := []struct {
		name string
		open bool
		want want
	}{
		{"close removes the open state", true, want{nil, relay.ErrNoOpen}},
		{"close fails without an open relay", false, want{relay.ErrNoOpen, relay.ErrNoOpen}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			now := time.Now()
			if tc.open {
				_, err := s.Open("s1", "origin", "", now)
				require.NoError(t, err)
			}
			_, closeErr := s.Close("s1", now)
			_, currentErr := s.Current("s1")
			assert.Equal(t, tc.want, want{closeErr, currentErr})
		})
	}
}

// A relay closed or a request settled keeps its scope until the turn ends or an hour passes
func TestStoreEnded(t *testing.T) {
	t.Parallel()
	type args struct {
		close   bool
		linger  bool
		endTurn bool
		after   time.Duration
	}
	type want struct {
		origin string
		target string
		found  bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"keeps a closed relay", args{close: true}, want{"o1", "pr1", true}},
		{"keeps a request settled without a relay", args{linger: true}, want{"o2", "pr2", true}},
		{"forgets it when the turn ends", args{close: true, endTurn: true}, want{"", "", false}},
		{"forgets it after an hour", args{close: true, after: 2 * time.Hour}, want{"", "", false}},
		{"has nothing before any close", args{}, want{"", "", false}},
		{"ends a turn with nothing to forget", args{endTurn: true}, want{"", "", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			now := time.Now()
			if tc.args.close {
				_, err := s.Open("s1", "o1", "pr1", now)
				require.NoError(t, err)
				_, err = s.Close("s1", now)
				require.NoError(t, err)
			}
			if tc.args.linger {
				require.NoError(t, s.Linger("s1", "o2", "pr2", now))
			}
			if tc.args.endTurn {
				require.NoError(t, s.EndTurn("s1"))
			}

			r, found, err := s.Ended("s1", now.Add(tc.args.after))

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{r.Origin, r.Target, found})
		})
	}
}

func TestStoreEvidence(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(t.TempDir(), "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	observed := locmap.Path{Name: "svc", Root: repo, Rel: "observed.go"}

	type want struct {
		rels   []string
		failed bool
	}
	tcs := []struct {
		name string
		raws []string
		want want
	}{
		{"falls back to observed paths", nil, want{[]string{"observed.go"}, false}},
		{"uses only given paths", []string{filepath.Join(repo, "given.go")}, want{[]string{"given.go"}, false}},
		{"skips a path outside a repository", []string{"/nowhere/x.go", filepath.Join(repo, "given.go")}, want{[]string{"given.go"}, false}},
		{"falls back when no path resolves", []string{"/nowhere/x.go"}, want{[]string{"observed.go"}, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			r, err := s.Open("s1", "origin", "", time.Now())
			require.NoError(t, err)
			require.NoError(t, s.Observe("s1", observed))

			ps, err := s.Evidence(r.Id, tc.raws, "")
			got := []string{}
			for _, p := range ps {
				got = append(got, p.Rel)
			}
			assert.Equal(t, tc.want, want{got, err != nil})
		})
	}
}

func TestStoreObserved_CorruptLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "relay", "observed", "r1.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("{broken\n"), 0o644))

	_, err := relay.New(dir).Observed("r1")

	assert.ErrorContains(t, err, "line 1 is corrupt")
}

func TestStoreOpen_PrunesStale(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		gap  time.Duration
		want error
	}{
		{"closes a relay left open for over a week", 8 * 24 * time.Hour, relay.ErrNoOpen},
		{"keeps a recent relay of another session", 24 * time.Hour, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			now := time.Now()
			_, err := s.Open("old", "o1", "", now)
			require.NoError(t, err)

			_, err = s.Open("new", "o2", "", now.Add(tc.gap))
			require.NoError(t, err)

			_, err = s.Current("old")
			assert.Equal(t, tc.want, err)
		})
	}
}

func TestStoreCurrent_Corrupt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "relay", "open", "s1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("{broken"), 0o644))

	_, err := relay.New(dir).Current("s1")

	assert.ErrorContains(t, err, "remove it to reset")
}
