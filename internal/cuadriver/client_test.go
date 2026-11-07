package cuadriver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/cuadriver"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "call" || !strings.HasPrefix(os.Getenv("LANG"), "fixture-") {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	switch os.Getenv("LANG") {
	case "fixture-success":
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Args  []string        `json:"args"`
			Input json.RawMessage `json:"input"`
			Env   []string        `json:"env"`
		}{os.Args[1:], input, os.Environ()})
	case "fixture-array":
		fmt.Fprint(os.Stdout, " [ 1, 2 ] \n")
	case "fixture-text":
		fmt.Fprint(os.Stdout, "not JSON")
	case "fixture-scalar":
		fmt.Fprint(os.Stdout, "true")
	case "fixture-exit":
		fmt.Fprint(os.Stdout, `{"effect":"unknown"}`)
		fmt.Fprint(os.Stderr, "private fixture text")
		os.Exit(1)
	case "fixture-timeout":
		time.Sleep(time.Minute)
	case "fixture-limit":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), (16<<20)+1))
	case "fixture-stderr":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), (16<<20)+1))
	case "fixture-mixed":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 9<<20))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 9<<20))
	}
	os.Exit(0)
}

func TestCall(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	window := `{"target":{"kind":"window","pid":1,"window_id":2}}`
	type args struct {
		mode, tool, input string
		timeout           time.Duration
		canceled, missing bool
	}
	type want struct {
		output           string
		started, failure bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"array", args{mode: "array", tool: "list_apps", input: "{}"}, want{"[1,2]", true, false}},
		{"text", args{mode: "text", tool: "list_apps", input: "{}"}, want{"", true, true}},
		{"scalar", args{mode: "scalar", tool: "list_apps", input: "{}"}, want{"", true, true}},
		{"nonzero", args{mode: "exit", tool: "click", input: window}, want{`{"effect":"unknown"}`, true, true}},
		{"timeout", args{mode: "timeout", tool: "click", input: window, timeout: 20 * time.Millisecond},
			want{"", true, true}},
		{"output limit", args{mode: "limit", tool: "list_apps", input: "{}"}, want{"", true, true}},
		{"stderr limit", args{mode: "stderr", tool: "list_apps", input: "{}"}, want{"", true, true}},
		{"combined limit", args{mode: "mixed", tool: "list_apps", input: "{}"}, want{"", true, true}},
		{"canceled", args{mode: "array", tool: "click", input: window, canceled: true}, want{"", false, true}},
		{"missing binary", args{mode: "array", tool: "click", input: window, missing: true}, want{"", false, true}},
		{"invalid JSON", args{mode: "array", tool: "click", input: "{"}, want{"", false, true}},
		{"array input", args{mode: "array", tool: "click", input: "[]"}, want{"", false, true}},
		{"null input", args{mode: "array", tool: "click", input: "null"}, want{"", false, true}},
		{"script denied", args{mode: "array", tool: "run_script", input: "{}"}, want{"", false, true}},
		{"desktop capture denied", args{mode: "array", tool: "get_desktop_state", input: "{}"},
			want{"", false, true}},
		{"desktop target denied", args{mode: "array", tool: "click", input: `{"target":{"kind":"desktop"}}`},
			want{"", false, true}},
		{"launch argv denied", args{mode: "array", tool: "launch_app", input: `{"additional_arguments":["x"]}`},
			want{"", false, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := binary
			if tc.args.missing {
				path = t.TempDir() + "/absent"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.args.canceled {
				cancel()
			}
			client := cuadriver.New(path, []string{"LANG=fixture-" + tc.args.mode}, tc.args.timeout)
			body, started, err := client.Call(ctx, tc.args.tool, []byte(tc.args.input))
			assert.Equal(t, tc.want, want{string(body), started, err != nil})
			assert.NotContains(t, fmt.Sprint(err), "private fixture text")
		})
	}
}

func TestCallWire(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	env := []string{"LANG=fixture-success", "PATH=fixture-path", "UNRELATED=excluded",
		"CUA_DRIVER_PERMISSION_MODE=unrestricted"}
	client := cuadriver.New(binary, env, 5*time.Second)
	env[1] = "PATH=mutated"
	body, started, err := client.Call(context.Background(), "type_text",
		[]byte(`{"target":{"kind":"window","pid":1,"window_id":2},"text":"a $(x) `+"`x`"+`"}`))
	require.NoError(t, err)
	assert.True(t, started)
	var got struct {
		Args  []string
		Input map[string]json.RawMessage
		Env   []string
	}
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, []string{"call", "type_text"}, got.Args)
	assert.JSONEq(t, `"a $(x) `+"`x`"+`"`, string(got.Input["text"]))
	assert.ElementsMatch(t, []string{"LANG=fixture-success", "PATH=fixture-path"}, got.Env)
}

func TestCallTargets(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type args struct{ tool, input string }
	type spec struct {
		name  string
		args  args
		valid bool
	}
	tcs := []spec{
		{"window capture", args{"get_window_state", `{"pid":1,"window_id":2}`}, true},
		{"foreground exact window", args{"bring_to_front", `{"pid":1,"window_id":2}`}, true},
		{"missing capture target", args{"get_window_state", `{}`}, false},
		{"missing capture window", args{"get_window_state", `{"pid":1}`}, false},
		{"zero capture owner", args{"get_window_state", `{"pid":0,"window_id":2}`}, false},
		{"negative capture window", args{"get_window_state", `{"pid":1,"window_id":-1}`}, false},
		{"invalid capture owner", args{"get_window_state", `{"pid":"1","window_id":2}`}, false},
		{"foreground without exact window", args{"bring_to_front", `{"pid":1}`}, false},
		{"conflicting owner", args{"click", `{"pid":3,"target":{"kind":"window","pid":1,"window_id":2}}`}, false},
		{"zero action window", args{"click", `{"target":{"kind":"window","pid":1,"window_id":0}}`}, false},
	}
	for _, tool := range []string{"click", "type_text", "press_key", "hotkey", "scroll"} {
		tcs = append(tcs, spec{
			tool + " exact target", args{tool, `{"target":{"kind":"window","pid":1,"window_id":2}}`}, true,
		}, spec{tool + " implicit target", args{tool, `{}`}, false})
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := cuadriver.New(binary, []string{"LANG=fixture-array"}, 5*time.Second)
			_, started, err := client.Call(context.Background(), tc.args.tool, []byte(tc.args.input))
			assert.Equal(t, tc.valid, started)
			assert.Equal(t, tc.valid, err == nil)
		})
	}
}

func TestCallForbiddenFields(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type args struct{ tool, input string }
	tcs := []struct {
		name string
		args args
	}{
		{"screenshot output", args{"get_window_state", `{"pid":1,"window_id":2,"screenshot_out_file":"x"}`}},
		{"debug output", args{"get_window_state", `{"pid":1,"window_id":2,"debug_image_out":"x"}`}},
		{"projection scope", args{"click", `{"target":{"kind":"window","pid":1,"window_id":2},"scope":"projection"}`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := cuadriver.New(binary, []string{"LANG=fixture-array"}, 5*time.Second)
			body, started, err := client.Call(context.Background(), tc.args.tool, []byte(tc.args.input))
			assert.ErrorIs(t, err, cuadriver.ErrInput)
			assert.False(t, started)
			assert.Empty(t, body)
		})
	}
}

func TestCallEnvironment(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type args struct{ value string }
	type want struct{ allowed bool }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"standard", args{"CUA_DRIVER_PERMISSION_MODE=standard"}, want{true}},
		{"telemetry zero", args{"CUA_DRIVER_RS_TELEMETRY_ENABLED=0"}, want{true}},
		{"telemetry false", args{"CUA_DRIVER_RS_TELEMETRY_ENABLED=false"}, want{true}},
		{"unrestricted", args{"CUA_DRIVER_PERMISSION_MODE=unrestricted"}, want{}},
		{"autonomous", args{"CUA_DRIVER_PERMISSION_MODE=autonomous"}, want{}},
		{"danger acknowledgement", args{"CUA_DRIVER_DANGEROUSLY_BYPASS_APPROVALS=1"}, want{}},
		{"telemetry enabled", args{"CUA_DRIVER_RS_TELEMETRY_ENABLED=1"}, want{}},
		{"unknown override", args{"CUA_DRIVER_AUTOGRANT=1"}, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := cuadriver.New(binary, []string{"LANG=fixture-success", tc.args.value}, 5*time.Second)
			body, _, err := client.Call(context.Background(), "list_apps", []byte(`{}`))
			require.NoError(t, err)
			var got struct{ Env []string }
			require.NoError(t, json.Unmarshal(body, &got))
			assert.Equal(t, tc.want.allowed, slices.Contains(got.Env, tc.args.value))
		})
	}
}

func TestCallRejectsUnresolvedExecutable(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.Base(binary))
	data, err := os.ReadFile(binary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0700))
	t.Setenv("PATH", dir)
	t.Chdir(dir)
	tcs := []struct {
		name, binary string
	}{
		{name: "basename", binary: filepath.Base(path)},
		{name: "relative", binary: "." + string(filepath.Separator) + filepath.Base(path)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			client := cuadriver.New(tc.binary, []string{"LANG=fixture-array", "PATH="}, time.Second)
			body, started, err := client.Call(context.Background(), "list_apps", []byte(`{}`))
			assert.ErrorIs(t, err, cuadriver.ErrCall)
			assert.False(t, started)
			assert.Empty(t, body)
		})
	}
}
