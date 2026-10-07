package plugin_test

import (
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

// A fake meetproxy that prints its name and arguments
func fakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "#!/bin/sh\n[ \"$1\" = version ] && echo 9.9.9 && exit 0\necho " + name + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meetproxy"), []byte(body), 0o755))
}

func TestLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launcher is a POSIX shell script")
	}
	t.Parallel()
	type args struct {
		cached bool
		onPath bool
		relay  string
		cmd    []string
		// none for no marker directory, empty or marked
		scope string
	}
	type want struct {
		code   int
		stdout string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"runs the cached binary", args{true, false, "none", []string{"hook", "path"}, "none"}, want{0, "cached hook path"}},
		{"falls back to PATH", args{false, true, "none", []string{"open", "x"}, "none"}, want{0, "path open x"}},
		{"guard blocks without a binary while a relay is open", args{false, false, "open", []string{"hook", "guard"}, "none"}, want{2, ""}},
		{"guard passes without a binary when no relay is open", args{false, false, "none", []string{"hook", "guard"}, "none"}, want{0, ""}},
		{"guard blocks without a binary or a data dir", args{false, false, "", []string{"hook", "guard"}, "none"}, want{2, ""}},
		{"path hook passes quietly without a binary", args{false, false, "none", []string{"hook", "path"}, "none"}, want{0, ""}},
		{"other commands fail without a binary", args{false, false, "none", []string{"version"}, "none"}, want{1, ""}},
		{"guard ends before the binary when no session has a scope", args{true, false, "none", []string{"hook", "guard"}, "empty"}, want{0, ""}},
		{"path hook ends before the binary when no session has a scope", args{true, false, "none", []string{"hook", "path"}, "empty"}, want{0, ""}},
		{"guard runs the binary while a session has a scope", args{true, false, "none", []string{"hook", "guard"}, "marked"}, want{0, "cached hook guard"}},
		{"guard blocks without a binary while a session has a scope", args{false, false, "none", []string{"hook", "guard"}, "marked"}, want{2, ""}},
		{"other hooks never end early", args{true, false, "none", []string{"hook", "stop"}, "empty"}, want{0, "cached hook stop"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, tools := launcher(t)
			home, pathDir := t.TempDir(), t.TempDir()
			if tc.args.cached {
				fakeBinary(t, filepath.Join(home, ".meetproxy", "bin", "v9.9.9"), "cached")
			}
			if tc.args.onPath {
				fakeBinary(t, pathDir, "path")
			}
			cmd := exec.Command(script, tc.args.cmd...)
			cmd.Env = []string{"HOME=" + home, "PATH=" + pathDir + string(os.PathListSeparator) + tools}
			if tc.args.relay != "" {
				data := t.TempDir()
				cmd.Env = append(cmd.Env, "CLAUDE_PLUGIN_DATA="+data)
				if tc.args.relay == "open" {
					open := filepath.Join(data, "relay", "open")
					require.NoError(t, os.MkdirAll(open, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(open, "s1.json"), []byte("{}"), 0o600))
				}
				if tc.args.scope != "none" {
					require.NoError(t, os.MkdirAll(filepath.Join(data, "scope"), 0o700))
				}
				if tc.args.scope == "marked" {
					require.NoError(t, os.WriteFile(filepath.Join(data, "scope", "s1"), []byte("s1"), 0o600))
				}
			}

			out, err := cmd.Output()

			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			}
			assert.Equal(t, tc.want, want{code, strings.TrimSpace(string(out))})
		})
	}
}
