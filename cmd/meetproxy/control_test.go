package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/control"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "call" || os.Getenv("LANG") != "control-fixture" {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	var arguments map[string]any
	if err != nil || json.Unmarshal(data, &arguments) != nil {
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}{os.Args[2], arguments}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestControlStandaloneJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local file journals require a Unix platform")
	}
	t.Setenv("LANG", "control-fixture")
	binary, err := os.Executable()
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	input := `{"id":"acme-op","session":"acme","tool":"start_session","arguments":{"text":"<>&"}}`
	call := []string{"call", "--driver", binary, "--data", dir}
	var first bytes.Buffer
	require.NoError(t, runControl(context.Background(), call, strings.NewReader(input), &first))
	tcs := []struct {
		name string
		args []string
	}{
		{name: "call", args: call},
		{name: "result without driver", args: []string{
			"result", "--driver", filepath.Join(dir, "absent"), "--data", dir, "acme-op",
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			require.NoError(t, runControl(context.Background(), tc.args, strings.NewReader(input), &output))
			assert.Contains(t, output.String(), `"text":"<>&"`)
			assert.NotContains(t, output.String(), `\u003c`)
			assert.True(t, strings.HasSuffix(output.String(), "\n"))
			var got control.Result
			require.NoError(t, json.Unmarshal(output.Bytes(), &got))
			assert.Equal(t, "completed", got.State)
			assert.True(t, got.Recorded)
			assert.JSONEq(t, `{"tool":"start_session","arguments":{"session":"acme","text":"<>&"}}`,
				string(got.Output))
			reader, writer := io.Pipe()
			require.NoError(t, reader.Close())
			t.Cleanup(func() { assert.NoError(t, writer.Close()) })
			assert.ErrorIs(t, runControl(context.Background(), tc.args, strings.NewReader(input), writer), io.ErrClosedPipe)
		})
	}
}

func TestControlExecutableSelection(t *testing.T) {
	t.Setenv("LANG", "control-fixture")
	binary, err := os.Executable()
	require.NoError(t, err)
	content, err := os.ReadFile(binary)
	require.NoError(t, err)
	dir := t.TempDir()
	name := "acme-driver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), content, 0700))
	t.Setenv("PATH", dir)
	t.Chdir(dir)
	tcs := []struct {
		name, binary string
		failure      bool
	}{
		{name: "basename", binary: name},
		{name: "relative", binary: "." + string(filepath.Separator) + name},
		{name: "missing", binary: filepath.Join(dir, "absent"), failure: true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runControl(context.Background(), []string{"call", "--driver", tc.binary, "--data", t.TempDir()},
				strings.NewReader(`{"session":"acme","tool":"list_apps","arguments":{}}`), &output)
			assert.Equal(t, tc.failure, err != nil)
			assert.Equal(t, !tc.failure, strings.Contains(output.String(), `"state":"completed"`))
		})
	}
}

func TestControlParity(t *testing.T) {
	t.Setenv("LANG", "control-fixture")
	binary, err := os.Executable()
	require.NoError(t, err)
	tcs := []struct {
		name    string
		request control.Request
	}{
		{name: "observation", request: control.Request{
			Session: "s1", Tool: "list_apps", Arguments: json.RawMessage(`{}`),
		}},
		{name: "effect", request: control.Request{
			ID: "op1", Session: "s1", Tool: "click", Arguments: json.RawMessage(
				`{"element_token":"e1","target":{"kind":"window","pid":1,"window_id":2}}`),
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.request.ID != "" {
				t.Skip("local file journals require a Unix platform")
			}
			ctx := context.Background()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			client, err := control.Open(control.Config{
				Binary: binary, Environment: []string{"LANG=control-fixture"}, Data: filepath.Join(dir, "api"),
			})
			require.NoError(t, err)
			api, err := client.Execute(ctx, tc.request)
			require.NoError(t, err)
			input, err := json.Marshal(tc.request)
			require.NoError(t, err)
			var output bytes.Buffer
			err = runControl(ctx, []string{"call", "--driver", binary, "--data", filepath.Join(dir, "cmd")},
				bytes.NewReader(input), &output)
			require.NoError(t, err)
			var command control.Result
			require.NoError(t, json.Unmarshal(output.Bytes(), &command))
			assert.Equal(t, api, command)
		})
	}
}

func TestControlInvalidRequest(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	tcs := []struct {
		name, input string
	}{
		{name: "unknown field", input: `{"session":"s1","tool":"list_apps","arguments":{},"extra":true}`},
		{name: "trailing value", input: `{} {}`},
		{name: "oversized request", input: `{"session":"` + strings.Repeat("x", 1<<20) + `"}`},
		{name: "trailing bytes exceed limit", input: `{"session":"s1","tool":"list_apps","arguments":{}}` +
			strings.Repeat(" ", 1<<20) + `{}`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			err := runControl(context.Background(), []string{"call", "--driver", binary, "--data", t.TempDir()},
				strings.NewReader(tc.input), &output)
			assert.Error(t, err)
			assert.Empty(t, output.String())
		})
	}
}
