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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tools the launcher uses before the PATH fallback, without gh
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

// A fake meetproxy that prints its name, its arguments and the hook input it got back from the launcher
// It runs on builtins alone so it works without any tool on PATH
func fakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "#!/bin/sh\n[ \"$1\" = version ] && echo 9.9.9 && exit 0\necho " + name + " \"$@\"\n" +
		"printf '%s' \"${MEETPROXY_HOOK_HEAD:-}\"\n" +
		"[ \"$1\" = hook ] && while IFS= read -r l || [ -n \"$l\" ]; do printf '%s\\n' \"$l\"; done\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meetproxy"), []byte(body), 0o755))
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

// A fake gh that logs every call and serves release files from RELEASE only with READABLE set
// With GH_HOST set it reads that host unless --repo names github.com, as gh does
// Its auth token prints ghToken only with LOGGED_IN set and its auth status always fails, as with a second broken account
// A fake curl and go only log, so a call of either shows in the log
const (
	ghToken = "gho_t0ken"
	fakeGh  = "#!/bin/sh\necho \"gh $*\" >> \"$CALLS\"\n" +
		"[ \"$1 $2\" = \"auth token\" ] && { [ -n \"$LOGGED_IN\" ] && echo " + ghToken + "; exit; }\n" +
		"[ \"$1 $2\" = \"release download\" ] && [ -n \"$READABLE\" ] || exit 1\n" +
		"dir= pats= repo=\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in\n    --dir) dir=$2; shift ;;\n    --pattern) pats=\"$pats $2\"; shift ;;\n    --repo) repo=$2; shift ;;\n  esac\n  shift\ndone\n" +
		"[ -n \"$GH_HOST\" ] && [ \"${repo%%/*}\" != github.com ] && exit 1\n" +
		"for p in $pats; do cat \"$RELEASE/$p\" > \"$dir/$p\" || exit 1; done\n"
	fakeLogger = "#!/bin/sh\necho \"$0 $*\" >> \"$CALLS\"\nexit 1\n"
)

// A release fetched from GitHub through the active gh login runs only when its sha256 matches checksums.txt
// A failed download says to log in only when gh has no token
func TestLauncher_ReleaseChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	t.Parallel()
	archive := "meetproxy_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	download := "gh release download v9.9.9 --repo github.com/jeon-jihyeon/meetproxy --pattern " + archive + " --pattern checksums.txt --dir "
	token := "gh auth token --hostname github.com"
	login := "meetproxy: run gh auth login, then run again"
	noRead := "through gh. Check that gh auth login can read jeon-jihyeon/meetproxy, then run again"
	tcs := []struct {
		name     string
		match    bool
		gh       bool
		logged   bool
		readable bool
		code     int
		stdout   string
		// The calls logged in order, a download matched by its start
		calls []string
		// A fixed line stderr holds
		stderr string
		// The GH_HOST of the user's shell
		host string
	}{
		{"a matching checksum runs the fetched binary", true, true, true, true, 0, "release open x", []string{download}, "", ""},
		{"a GH_HOST of another host still fetches from github.com", true, true, true, true, 0, "release open x", []string{download}, "", "ghe.example.com"},
		{"a mismatch exits before anything runs", false, true, true, true, 1, "", []string{download}, "", ""},
		{"a login that can't read the release says to check its access", true, true, true, false, 1, "", []string{download, token}, noRead, ""},
		{"no gh login says to log in", true, true, false, false, 1, "", []string{download, token}, login, ""},
		{"no gh fetches nothing", true, false, false, false, 1, "", nil, login, ""},
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
			if tc.gh {
				require.NoError(t, os.WriteFile(filepath.Join(tools, "gh"), []byte(fakeGh), 0o755))
			}
			for _, name := range []string{"curl", "go"} {
				require.NoError(t, os.WriteFile(filepath.Join(tools, name), []byte(fakeLogger), 0o755))
			}
			calls := filepath.Join(t.TempDir(), "calls")
			env := []string{"HOME=" + home, "PATH=" + tools, "RELEASE=" + release, "CALLS=" + calls}
			if tc.logged {
				env = append(env, "LOGGED_IN=1")
			}
			if tc.readable {
				env = append(env, "READABLE=1")
			}
			if tc.host != "" {
				env = append(env, "GH_HOST="+tc.host)
			}

			cmd := exec.Command(script, "open", "x")
			cmd.Env = env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()

			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			}
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.stdout, strings.TrimSpace(string(out)))
			_, statErr := os.Stat(filepath.Join(home, ".meetproxy", "bin", "v9.9.9", "meetproxy"))
			assert.Equal(t, tc.match && tc.readable, statErr == nil, "only a verified binary is kept")
			logged, _ := os.ReadFile(calls)
			lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
			if len(logged) == 0 {
				lines = nil
			}
			require.Len(t, lines, len(tc.calls), string(logged))
			for i, want := range tc.calls {
				assert.True(t, strings.HasPrefix(lines[i], want), lines[i])
			}
			if tc.stderr != "" {
				assert.Contains(t, stderr.String(), tc.stderr)
			}
			if tc.stderr != login {
				assert.NotContains(t, stderr.String(), login)
			}
			assert.NotContains(t, string(out)+stderr.String(), ghToken, "the gh token goes to no output")
		})
	}
}

func TestLauncher_ApplicationChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	t.Parallel()
	type want struct {
		output string
		build  string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"a command rebuilds an existing binary", []string{"open", "x"}, want{"rebuilt open x", "build"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			root := filepath.Dir(filepath.Dir(filepath.Dir(script)))
			for _, name := range []string{"app", "cmd", "internal"} {
				require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o755))
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
				[]byte("module github.com/jeon-jihyeon/meetproxy\n"), 0o600))
			file := filepath.Join(root, "app", "policy.go")
			require.NoError(t, os.WriteFile(file, []byte("package app\n"), 0o600))
			fakeBinary(t, filepath.Join(root, "bin"), "cached")
			before := time.Now().Add(-time.Hour)
			for _, name := range []string{"app", "cmd", "internal", "go.mod", "app/policy.go"} {
				require.NoError(t, os.Chtimes(filepath.Join(root, name), before, before))
			}
			after := time.Now().Add(time.Minute)
			require.NoError(t, os.Chtimes(file, after, after))
			build := "#!/bin/sh\necho build >> \"$CALLS\"\n" +
				"printf '%s\\n' '#!/bin/sh' 'echo rebuilt \"$@\"' > \"$3\"\nchmod 755 \"$3\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(tools, "go"), []byte(build), 0o755))
			calls := filepath.Join(root, "calls")
			require.NoError(t, os.WriteFile(calls, nil, 0o600))
			cmd := exec.Command(script, tc.args...)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + tools, "CALLS=" + calls}

			out, err := cmd.Output()

			require.NoError(t, err)
			assert.Equal(t, tc.want.output, strings.TrimSpace(string(out)))
			logged, err := os.ReadFile(calls)
			require.NoError(t, err)
			assert.Equal(t, tc.want.build, strings.TrimSpace(string(logged)))
		})
	}
}
