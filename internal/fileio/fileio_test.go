package fileio_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

func TestWriteAtomic(t *testing.T) {
	t.Parallel()
	type want struct {
		data string
		perm os.FileMode
	}
	tcs := []struct {
		name   string
		before string
		data   string
		perm   os.FileMode
		want   want
	}{
		{"creates the file and its directory", "", "a", 0o644, want{"a", 0o644}},
		{"replaces an existing file", "old", "new", 0o644, want{"new", 0o644}},
		{"sets the permission", "", "a", 0o600, want{"a", 0o600}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "d", "f")
			if tc.before != "" {
				require.NoError(t, fileio.WriteAtomic(path, []byte(tc.before), 0o644))
			}

			require.NoError(t, fileio.WriteAtomic(path, []byte(tc.data), tc.perm))

			b, err := os.ReadFile(path)
			require.NoError(t, err)
			info, err := os.Stat(path)
			require.NoError(t, err)
			entries, err := os.ReadDir(filepath.Dir(path))
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{string(b), info.Mode().Perm()})
			assert.Len(t, entries, 1)
		})
	}
}

// A reader running beside the writer only ever sees one whole version
func TestWriteAtomic_NoPartialRead(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	versions := []string{strings.Repeat("a", 1<<20), strings.Repeat("b", 1<<20)}
	require.NoError(t, fileio.WriteAtomic(path, []byte(versions[0]), 0o644))

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := range 50 {
			assert.NoError(t, fileio.WriteAtomic(path, []byte(versions[i%2]), 0o644))
		}
	}()
	var seen []string
	for {
		select {
		case <-done:
			wg.Wait()
			assert.Subset(t, versions, seen)
			return
		default:
		}
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		seen = append(seen, string(b))
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	type doc struct {
		Name string `json:"name"`
	}
	type want struct {
		found bool
		doc   doc
		file  string
		err   bool
	}
	tcs := []struct {
		name  string
		write bool
		raw   string
		want  want
	}{
		{"round trips indented JSON", true, "", want{true, doc{"x"}, "{\n  \"name\": \"x\"\n}\n", false}},
		{"reports a missing file as not found", false, "", want{false, doc{}, "", false}},
		{"fails on a corrupt file", false, "{", want{true, doc{}, "{", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "f.json")
			if tc.write {
				require.NoError(t, fileio.WriteJSON(path, doc{"x"}))
			}
			if tc.raw != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.raw), 0o644))
			}

			var got doc
			found, err := fileio.ReadJSON(path, &got)

			b, _ := os.ReadFile(path)
			assert.Equal(t, tc.want, want{found, got, string(b), err != nil})
		})
	}
}

// Each goroutine opens its own descriptor so flock serializes them as it does separate processes
func TestLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "d", "lock")
	var wg sync.WaitGroup
	var mu sync.Mutex
	inside, most := 0, 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := fileio.Lock(path)
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			inside++
			most = max(most, inside)
			mu.Unlock()
			for range 1000 {
				_, _ = os.Stat(path)
			}
			mu.Lock()
			inside--
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, most)
}

// A second locker gives up while the first holds the lock and takes it once released
func TestTryLock(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "lock")

	first, ok, err := fileio.TryLock(path)
	require.NoError(t, err)
	require.True(t, ok)
	_, held, err := fileio.TryLock(path)
	require.NoError(t, err)
	first()
	second, freed, err := fileio.TryLock(path)
	require.NoError(t, err)
	second()

	assert.Equal(t, []bool{false, true}, []bool{held, freed})
}

func TestAppendLine(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "d", "log")
	line := bytes.Repeat([]byte("x"), 100)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, fileio.AppendLine(path, line))
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat(string(line)+"\n", 20), string(b))
}
