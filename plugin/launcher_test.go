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
		// none for no marker directory, empty, marked for s1 or other for s2
		scope string
		// Hook input on stdin
		stdin string
		// Runs with nothing on PATH so a fork of any tool fails
		bare bool
	}
	type want struct {
		code   int
		stdout string
	}
	guard, stop := []string{"hook", "guard"}, []string{"hook", "stop"}
	input := `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`
	// Longer than the head the launcher reads and with backslashes and line breaks it must keep
	long := `{"session_id":"s1","tool_name":"Write","tool_input":{"content":"` +
		strings.Repeat(`a\\b\"c `, 400) + `"}}` + "\n  {}"
	nested := `{"tool_input":{"session_id":"s9"},"session_id":"s1"}`
	rewritten := `{"session_id":"s.1"}`
	hook := func(cached bool, cmd []string, scope, stdin string) args {
		return args{cached: cached, relay: "none", cmd: cmd, scope: scope, stdin: stdin}
	}
	bare := func(a args) args {
		a.bare = true
		return a
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"runs the cached binary",
			args{cached: true, relay: "none", cmd: []string{"open", "x"}, scope: "none"}, want{0, "cached open x"},
		},
		{
			"falls back to PATH",
			args{onPath: true, relay: "none", cmd: []string{"open", "x"}, scope: "none"}, want{0, "path open x"},
		},
		{"guard blocks without a binary while a relay is open", args{relay: "open", cmd: guard, scope: "none"}, want{2, ""}},
		{"guard passes without a binary when no session has a scope", hook(false, guard, "empty", ""), want{0, ""}},
		{"guard blocks without a binary or a marker directory", args{relay: "inbox", cmd: guard, scope: "none"}, want{2, ""}},
		{"guard passes without a binary on a fresh install", hook(false, guard, "none", ""), want{0, ""}},
		{"guard blocks without a binary or a data dir", args{cmd: guard, scope: "none"}, want{2, ""}},
		{"stop hook passes quietly without a binary", hook(false, stop, "none", input), want{0, ""}},
		{"other commands fail without a binary", args{relay: "none", cmd: []string{"version"}, scope: "none"}, want{1, ""}},
		{"guard ends before the binary when no session has a scope", hook(true, guard, "empty", input), want{0, ""}},
		{"stop hook ends before the binary when no session has a scope", hook(true, stop, "empty", input), want{0, ""}},
		{
			"guard ends before the binary when only another session has a scope",
			hook(true, guard, "other", input), want{0, ""},
		},
		{
			"stop hook ends before the binary when only another session has a scope",
			hook(true, stop, "other", input), want{0, ""},
		},
		{
			"guard ends without forking any tool when only another session has a scope",
			bare(hook(true, guard, "other", input)), want{0, ""},
		},
		{
			"guard hands the whole input to the binary while the session has a scope",
			hook(true, guard, "marked", long), want{0, "cached hook guard\n" + long},
		},
		{
			"guard runs the binary without forking any tool while the session has a scope",
			bare(hook(true, guard, "marked", input)), want{0, "cached hook guard\n" + input},
		},
		{
			"guard leaves an input that does not start with the session to the binary",
			hook(true, guard, "other", nested), want{0, "cached hook guard\n" + nested},
		},
		{
			"guard leaves a session id a marker name rewrites to the binary",
			hook(true, guard, "other", rewritten), want{0, "cached hook guard\n" + rewritten},
		},
		{"guard blocks without a binary while the session has a scope", hook(false, guard, "marked", input), want{2, ""}},
		{
			"guard blocks without a binary when the input names no session and another has a scope",
			hook(false, guard, "other", "{}"), want{2, ""},
		},
		{
			"stop hook runs the binary while the session has a scope",
			hook(true, stop, "marked", input), want{0, "cached hook stop\n" + input},
		},
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
			cmd.Stdin = strings.NewReader(tc.args.stdin)
			cmd.Env = []string{"HOME=" + home, "PATH=" + pathDir + string(os.PathListSeparator) + tools}
			if tc.args.bare {
				cmd.Env = []string{"HOME=" + home, "PATH=" + t.TempDir()}
			}
			if tc.args.relay != "" {
				data := t.TempDir()
				cmd.Env = append(cmd.Env, "CLAUDE_PLUGIN_DATA="+data)
				if tc.args.relay == "open" {
					open := filepath.Join(data, "relay", "open")
					require.NoError(t, os.MkdirAll(open, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(open, "s1.json"), []byte("{}"), 0o600))
				}
				if tc.args.relay == "inbox" {
					require.NoError(t, os.MkdirAll(filepath.Join(data, "inbox"), 0o700))
				}
				if tc.args.scope != "none" {
					require.NoError(t, os.MkdirAll(filepath.Join(data, "scope"), 0o700))
				}
				marker := map[string]string{"marked": "s1", "other": "s2"}[tc.args.scope]
				if marker != "" {
					require.NoError(t, os.WriteFile(filepath.Join(data, "scope", marker), []byte(marker), 0o600))
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
