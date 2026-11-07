//go:build !windows

package socket_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/socket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadToken(t *testing.T) {
	t.Parallel()
	type args struct {
		body string
		dir  os.FileMode
		mode os.FileMode
		link bool
	}
	tcs := []struct {
		name string
		args args
		fail bool
	}{
		{"private token", args{"xapp-fixture\n", 0o700, 0o600, false}, false},
		{"public directory", args{"xapp-fixture", 0o755, 0o600, false}, true},
		{"public token", args{"xapp-fixture", 0o700, 0o644, false}, true},
		{"symlink token", args{"xapp-fixture", 0o700, 0o600, true}, true},
		{"wrong kind", args{"xoxb-fixture", 0o700, 0o600, false}, true},
		{"two tokens", args{"xapp-fixture xapp-other", 0o700, 0o600, false}, true},
		{"empty token", args{"xapp-", 0o700, 0o600, false}, true},
		{"control character", args{"xapp-fixture\x00", 0o700, 0o600, false}, true},
		{"oversized", args{"xapp-" + string(make([]byte, 4096)), 0o700, 0o600, false}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, tc.args.dir))
			target := filepath.Join(dir, "original")
			require.NoError(t, os.WriteFile(target, []byte(tc.args.body), tc.args.mode))
			path := target
			if tc.args.link {
				path = filepath.Join(dir, "token")
				require.NoError(t, os.Symlink(target, path))
			}

			token, err := socket.ReadToken(path)

			assert.Equal(t, tc.fail, err != nil)
			assert.Equal(t, !tc.fail, token == "xapp-fixture")
		})
	}
}

func TestAcquireSecurity(t *testing.T) {
	t.Parallel()
	type args struct {
		mode os.FileMode
	}
	tcs := []struct {
		name string
		args args
		init func(*testing.T, string, string) string
	}{
		{"public runtime", args{0o755}, func(_ *testing.T, _, dir string) string { return dir }},
		{"symlink runtime", args{0o700}, func(t *testing.T, parent, dir string) string {
			alias := filepath.Join(parent, "alias")
			require.NoError(t, os.Symlink(dir, alias))
			return alias
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			dir := filepath.Join(parent, "runtime")
			require.NoError(t, os.Mkdir(dir, tc.args.mode))
			dir = tc.init(t, parent, dir)

			lock, err := socket.Acquire(dir)

			assert.ErrorIs(t, err, socket.ErrSecurity)
			assert.Nil(t, lock)
		})
	}
}

func TestAcquireLifetime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	lock, err := socket.Acquire(dir)
	require.NoError(t, err)
	_, err = socket.Acquire(dir)
	assert.ErrorIs(t, err, socket.ErrBusy)
	require.NoError(t, lock.Close())
	lock, err = socket.Acquire(dir)
	require.NoError(t, err)
	assert.NoError(t, lock.Close())
	info, err := os.Stat(filepath.Join(dir, "lock"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestAcquireRejectsLockLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	target := filepath.Join(dir, "other")
	require.NoError(t, os.WriteFile(target, []byte("fixture"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "lock")))

	_, err := socket.Acquire(dir)

	assert.ErrorIs(t, err, socket.ErrSecurity)
	value, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "fixture", string(value))
}

func TestReadTokenRejectsParentLink(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "private")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte("xapp-fixture"), 0o600))
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(dir, alias))

	token, err := socket.ReadToken(filepath.Join(alias, "token"))

	assert.ErrorIs(t, err, socket.ErrSecurity)
	assert.Empty(t, token)
}
