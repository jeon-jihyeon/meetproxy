//go:build !windows

package opstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/opstore"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "opstore-claim" {
		return
	}
	_, created, err := opstore.New(os.Args[2]).Claim(context.Background(), "operation", "fingerprint", []byte("unknown"))
	if err != nil {
		os.Exit(1)
	}
	fmt.Fprint(os.Stdout, created)
	os.Exit(0)
}

func TestStore(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"operation", "../../outside", "unicode-요청"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			dir := canonicalDir(t)
			store := opstore.New(filepath.Join(dir, "operations"))
			ctx := context.Background()
			body, exists, err := store.Read(ctx, id)
			require.NoError(t, err)
			assert.False(t, exists)
			assert.Nil(t, body)
			body, created, err := store.Claim(ctx, id, "fingerprint", []byte("unknown"))
			require.NoError(t, err)
			assert.True(t, created)
			assert.Equal(t, "unknown", string(body))
			body, created, err = store.Claim(ctx, id, "fingerprint", []byte("different intent"))
			require.NoError(t, err)
			assert.False(t, created)
			assert.Equal(t, "unknown", string(body))
			require.NoError(t, store.Save(ctx, id, "fingerprint", []byte("completed")))
			body, created, err = store.Claim(ctx, id, "fingerprint", []byte("unknown"))
			require.NoError(t, err)
			assert.False(t, created)
			assert.Equal(t, "completed", string(body))
			_, _, err = store.Claim(ctx, id, "other fingerprint", []byte("unknown"))
			assert.ErrorIs(t, err, opstore.ErrConflict)
			assert.ErrorIs(t, store.Save(ctx, id, "other fingerprint", []byte("bad")), opstore.ErrConflict)
			body, exists, err = store.Read(ctx, id)
			require.NoError(t, err)
			assert.True(t, exists)
			assert.Equal(t, "completed", string(body))
			entries, err := os.ReadDir(filepath.Join(dir, "operations"))
			require.NoError(t, err)
			for _, entry := range entries {
				info, err := entry.Info()
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}
			info, err := os.Stat(filepath.Join(dir, "operations"))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			intent, err := os.ReadFile(filepath.Join(dir, "operations", hash(id)+".json"))
			require.NoError(t, err)
			var row struct{ Value []byte }
			require.NoError(t, json.Unmarshal(intent, &row))
			assert.Equal(t, "unknown", string(row.Value))
		})
	}
}

func TestStoreInvalid(t *testing.T) {
	t.Parallel()
	type args struct {
		id, fingerprint string
		size            int
		canceled        bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"empty id", args{fingerprint: "fingerprint", size: 1}},
		{"long id", args{id: string(bytes.Repeat([]byte("x"), 4097)), fingerprint: "fingerprint", size: 1}},
		{"empty fingerprint", args{id: "operation", size: 1}},
		{"empty value", args{id: "operation", fingerprint: "fingerprint"}},
		{"large value", args{id: "operation", fingerprint: "fingerprint", size: (16 << 20) + (1 << 10) + 1}},
		{"canceled", args{id: "operation", fingerprint: "fingerprint", size: 1, canceled: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := opstore.New(canonicalDir(t))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.args.canceled {
				cancel()
			}
			_, created, err := store.Claim(ctx, tc.args.id, tc.args.fingerprint, bytes.Repeat([]byte("x"), tc.args.size))
			assert.Error(t, err)
			assert.False(t, created)
			assert.Error(t, store.Save(ctx, tc.args.id, tc.args.fingerprint, bytes.Repeat([]byte("x"), tc.args.size)))
		})
	}
}

func TestStoreValueBudget(t *testing.T) {
	dir := canonicalDir(t)
	ctx := context.Background()
	store := opstore.New(dir)
	_, created, err := store.Claim(ctx, "operation", "fingerprint", []byte("unknown"))
	require.NoError(t, err)
	require.True(t, created)
	value := bytes.Repeat([]byte("x"), (16<<20)+(1<<10))
	require.NoError(t, store.Save(ctx, "operation", "fingerprint", value))
	body, exists, err := opstore.New(dir).Read(ctx, "operation")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, value, body)
	assert.ErrorIs(t, store.Save(ctx, "operation", "fingerprint", append(value, 'x')), opstore.ErrInput)
}

func TestStoreSymlinks(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"root", "parent", "lock", "intent", "result"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			base := canonicalDir(t)
			dir := filepath.Join(base, "store")
			outside := filepath.Join(base, "outside")
			require.NoError(t, os.Mkdir(outside, 0o700))
			marker := filepath.Join(outside, "marker")
			require.NoError(t, os.WriteFile(marker, []byte("untouched"), 0o600))
			require.NoError(t, os.Mkdir(dir, 0o700))
			switch target {
			case "root":
				require.NoError(t, os.Remove(dir))
				require.NoError(t, os.Symlink(outside, dir))
			case "parent":
				link := filepath.Join(base, "parent")
				require.NoError(t, os.Symlink(base, link))
				dir = filepath.Join(link, "store")
			case "lock":
				require.NoError(t, os.Symlink(marker, filepath.Join(dir, "lock")))
			case "intent":
				require.NoError(t, os.Symlink(marker, filepath.Join(dir, hash("operation")+".json")))
			case "result":
				_, _, err := opstore.New(dir).Claim(context.Background(), "operation", "fingerprint", []byte("unknown"))
				require.NoError(t, err)
				require.NoError(t, os.Symlink(marker, filepath.Join(dir, hash("operation")+".result.json")))
			}
			store := opstore.New(dir)
			_, _, err := store.Claim(context.Background(), "operation", "fingerprint", []byte("unknown"))
			assert.Error(t, err)
			assert.Error(t, store.Save(context.Background(), "operation", "fingerprint", []byte("completed")))
			_, _, err = store.Read(context.Background(), "operation")
			assert.Error(t, err)
			body, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.Equal(t, "untouched", string(body))
		})
	}
}

func TestSaveFailureKeepsIntent(t *testing.T) {
	t.Parallel()
	dir := canonicalDir(t)
	store := opstore.New(dir)
	ctx := context.Background()
	_, _, err := store.Claim(ctx, "operation", "fingerprint", []byte("unknown"))
	require.NoError(t, err)
	path := filepath.Join(dir, hash("operation")+".json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	blocked := filepath.Join(dir, hash("operation")+".result.json")
	require.NoError(t, os.Mkdir(blocked, 0o700))
	assert.Error(t, store.Save(ctx, "operation", "fingerprint", []byte("completed")))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NoError(t, os.Remove(blocked))
	body, exists, err := store.Read(ctx, "operation")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, "unknown", string(body))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestClaimConcurrent(t *testing.T) {
	t.Parallel()
	dir := canonicalDir(t)
	var created atomic.Int32
	var group sync.WaitGroup
	for range 24 {
		group.Add(1)
		go func() {
			defer group.Done()
			body, fresh, err := opstore.New(dir).Claim(context.Background(), "operation", "fingerprint", []byte("unknown"))
			assert.NoError(t, err)
			assert.Equal(t, "unknown", string(body))
			if fresh {
				created.Add(1)
			}
		}()
	}
	group.Wait()
	assert.Equal(t, int32(1), created.Load())
}

func TestOrphanResult(t *testing.T) {
	t.Parallel()
	dir := canonicalDir(t)
	store := opstore.New(dir)
	ctx := context.Background()
	_, _, err := store.Claim(ctx, "operation", "fingerprint", []byte("unknown"))
	require.NoError(t, err)
	require.NoError(t, store.Save(ctx, "operation", "fingerprint", []byte("completed")))
	require.NoError(t, os.Remove(filepath.Join(dir, hash("operation")+".json")))
	_, created, err := store.Claim(ctx, "operation", "fingerprint", []byte("unknown"))
	assert.Error(t, err)
	assert.False(t, created)
	assert.Error(t, store.Save(ctx, "operation", "fingerprint", []byte("replacement")))
}

func TestResultFingerprint(t *testing.T) {
	t.Parallel()
	dir := canonicalDir(t)
	store := opstore.New(dir)
	ctx := context.Background()
	_, _, err := store.Claim(ctx, "operation", "fingerprint", []byte("unknown"))
	require.NoError(t, err)
	path := filepath.Join(dir, hash("operation")+".result.json")
	body := []byte(`{"fingerprint":"different","value":"Y29tcGxldGVk"}`)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	_, _, err = store.Read(ctx, "operation")
	assert.ErrorIs(t, err, opstore.ErrConflict)
	assert.ErrorIs(t, store.Save(ctx, "operation", "fingerprint", []byte("completed")), opstore.ErrConflict)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, after)
}

func TestClaimProcesses(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	dir := canonicalDir(t)
	commands := make([]*exec.Cmd, 6)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		commands[i] = exec.Command(binary, "opstore-claim", dir)
		commands[i].Env = []string{}
		commands[i].Stdout = &outputs[i]
		require.NoError(t, commands[i].Start())
	}
	created := 0
	for i, command := range commands {
		require.NoError(t, command.Wait())
		if outputs[i].String() == "true" {
			created++
		}
	}
	assert.Equal(t, 1, created)
}

func canonicalDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func hash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
