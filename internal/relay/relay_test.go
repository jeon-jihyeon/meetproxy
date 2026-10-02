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
		first  string
		second string
	}
	type want struct {
		sameId  bool
		origin  string
		observe []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"continues the same origin and keeps observations", args{"o1", "o1"}, want{true, "o1", []string{"a.go"}}},
		{"opens a new relay for another origin", args{"o1", "o2"}, want{false, "o2", []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			now := time.Now()
			first, err := s.Open("s1", tc.args.first, now)
			require.NoError(t, err)
			require.NoError(t, s.Observe("s1", locmap.Path{Root: "/r", Rel: "a.go"}))

			second, err := s.Open("s1", tc.args.second, now)
			require.NoError(t, err)
			ps, err := s.Observed(second.Id)
			require.NoError(t, err)
			got := []string{}
			for _, p := range ps {
				got = append(got, p.Rel)
			}
			assert.Equal(t, tc.want, want{first.Id == second.Id, second.Origin, got})
		})
	}
}

func TestStoreObserve(t *testing.T) {
	t.Parallel()
	a := locmap.Path{Repo: "svc", Root: "/r", Rel: "a.go"}
	b := locmap.Path{Repo: "svc", Root: "/r", Rel: "b.go"}

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
				r, err := s.Open("s1", "origin", time.Now())
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
				_, err := s.Open("s1", "origin", now)
				require.NoError(t, err)
			}
			_, closeErr := s.Close("s1", now)
			_, currentErr := s.Current("s1")
			assert.Equal(t, tc.want, want{closeErr, currentErr})
		})
	}
}

func TestStoreEvidence(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(t.TempDir(), "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	observed := locmap.Path{Repo: "svc", Root: repo, Rel: "observed.go"}

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
		{"fails on a path outside a repository", []string{"/nowhere/x.go"}, want{[]string{}, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := relay.New(t.TempDir())
			r, err := s.Open("s1", "origin", time.Now())
			require.NoError(t, err)
			require.NoError(t, s.Observe("s1", observed))

			ps, err := s.Evidence(r.Id, tc.raws)
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
