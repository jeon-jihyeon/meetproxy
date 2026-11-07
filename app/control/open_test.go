package control_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "call" || os.Getenv("LANG") != "control-boundary-fixture" {
		return
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "dispatched"), []byte("yes"), 0600); err != nil {
		os.Exit(1)
	}
	_, _ = io.WriteString(os.Stdout, `{}`)
	os.Exit(0)
}

func TestOpenExecutableBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local file journals require a Unix platform")
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	content, err := os.ReadFile(binary)
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	path := filepath.Join(dir, "acme-driver")
	require.NoError(t, os.WriteFile(path, content, 0700))
	t.Setenv("PATH", dir)
	t.Chdir(dir)
	tcs := []struct {
		name   string
		binary string
	}{
		{name: "ambient basename", binary: "acme-driver"},
		{name: "explicit relative path", binary: "./acme-driver"},
		{name: "missing absolute path", binary: filepath.Join(dir, "absent")},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			marker := t.TempDir()
			env := []string{"LANG=control-boundary-fixture", "PATH=", "TMPDIR=" + marker}
			client, err := control.Open(control.Config{
				Binary: tc.binary, Environment: env, Data: filepath.Join(dir, tc.name),
			})
			require.NoError(t, err)
			request := control.Request{ID: "acme-op", Session: "acme", Tool: "start_session",
				Arguments: json.RawMessage(`{}`)}
			result, err := client.Execute(context.Background(), request)
			require.Error(t, err)
			assert.EqualError(t, err, "driver call failed")
			assert.Equal(t, control.Result{
				ID: request.ID, Tool: request.Tool, State: "not_started", Recorded: true,
			}, result)
			recovered, err := control.Open(control.Config{Data: filepath.Join(dir, tc.name)})
			require.NoError(t, err)
			found, exists, err := recovered.Lookup(context.Background(), request.ID)
			require.NoError(t, err)
			assert.True(t, exists)
			assert.Equal(t, result, found)
			available, err := control.Open(control.Config{
				Binary: path, Environment: env, Data: filepath.Join(dir, tc.name),
			})
			require.NoError(t, err)
			replayed, err := available.Execute(context.Background(), request)
			require.NoError(t, err)
			assert.Equal(t, result, replayed)
			_, err = os.Stat(filepath.Join(marker, "dispatched"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestOpenAbsoluteExecutableWithoutAmbientPath(t *testing.T) {
	t.Setenv("PATH", "")
	binary, err := os.Executable()
	require.NoError(t, err)
	marker := t.TempDir()
	client, err := control.Open(control.Config{
		Binary: binary, Environment: []string{"LANG=control-boundary-fixture", "PATH=", "TMPDIR=" + marker},
	})
	require.NoError(t, err)
	result, err := client.Execute(context.Background(), control.Request{
		Session: "acme", Tool: "list_apps", Arguments: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	assert.Equal(t, control.Result{Tool: "list_apps", State: "completed", Output: json.RawMessage(`{}`)}, result)
	data, err := os.ReadFile(filepath.Join(marker, "dispatched"))
	require.NoError(t, err)
	assert.Equal(t, "yes", string(data))
}
