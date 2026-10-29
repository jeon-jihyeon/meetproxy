package plugin_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tools the launcher uses before the PATH fallback, without curl and go
var launcherTools = []string{"awk", "cat", "chmod", "cut", "dirname", "find", "grep", "head", "ls", "mkdir", "mktemp", "mv", "rm", "sed", "tar", "tr", "uname"}

// A launcher copy outside the source tree with a version no release has
func launcher(t *testing.T) (script, tools string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugin")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755))
	b, err := os.ReadFile(filepath.Join("bin", "meetproxy"))
	require.NoError(t, err)
	script = filepath.Join(root, "bin", "meetproxy")
	require.NoError(t, os.WriteFile(script, b, 0o755))
	manifest := []byte("{\n  \"version\": \"9.9.9\"\n}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), manifest, 0o600))
	tools = t.TempDir()
	for _, name := range launcherTools {
		path, err := exec.LookPath(name)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(tools, name)))
	}
	return script, tools
}

// A release archive of a fake meetproxy that prints release and its arguments
func releaseArchive(t *testing.T) []byte {
	t.Helper()
	body := []byte("#!/bin/sh\necho release \"$@\"\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "meetproxy", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// A release fetched from GitHub runs only when its sha256 matches checksums.txt
// A fake curl serves the release files from a directory by the last part of the URL
func TestLauncher_ReleaseChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	t.Parallel()
	archive := "meetproxy_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	tcs := []struct {
		name   string
		match  bool
		code   int
		stdout string
	}{
		{"a matching checksum runs the fetched binary", true, 0, "release open x"},
		{"a mismatch exits before anything runs", false, 1, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			sum := ""
			for _, name := range []string{"sha256sum", "shasum"} {
				if path, err := exec.LookPath(name); err == nil {
					require.NoError(t, os.Symlink(path, filepath.Join(tools, name)))
					sum = name
				}
			}
			if sum == "" {
				t.Skip("no sha256sum or shasum on this machine")
			}
			release, home := t.TempDir(), t.TempDir()
			b := releaseArchive(t)
			require.NoError(t, os.WriteFile(filepath.Join(release, archive), b, 0o600))
			digest := sha256.Sum256(b)
			want := hex.EncodeToString(digest[:])
			if !tc.match {
				want = strings.Repeat("0", len(want))
			}
			require.NoError(t, os.WriteFile(filepath.Join(release, "checksums.txt"), []byte(want+"  "+archive+"\n"), 0o600))
			curl := "#!/bin/sh\nurl=$2\ncat \"$RELEASE/${url##*/}\" > \"$4\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(tools, "curl"), []byte(curl), 0o755))

			cmd := exec.Command(script, "open", "x")
			cmd.Env = []string{"HOME=" + home, "PATH=" + tools, "RELEASE=" + release}
			out, err := cmd.Output()

			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			}
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.stdout, strings.TrimSpace(string(out)))
			_, statErr := os.Stat(filepath.Join(home, ".meetproxy", "bin", "v9.9.9", "meetproxy"))
			assert.Equal(t, tc.match, statErr == nil, "only a verified binary is kept")
		})
	}
}
