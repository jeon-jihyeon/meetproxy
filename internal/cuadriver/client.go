package cuadriver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const outputLimit = 16 << 20

var (
	ErrInput  = errors.New("invalid driver input")
	ErrTool   = errors.New("unsupported driver tool")
	ErrOutput = errors.New("invalid driver output")
	ErrLimit  = errors.New("driver output limit exceeded")
	ErrCall   = errors.New("driver call failed")
)

type Client struct {
	binary  string
	env     []string
	timeout time.Duration
}

func New(binary string, env []string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	kept := make([]string, 0, len(env))
	for _, value := range env {
		if allowedEnvironment(value) {
			kept = append(kept, value)
		}
	}
	return &Client{binary: binary, env: kept, timeout: timeout}
}

func allowedEnvironment(value string) bool {
	key, setting, found := strings.Cut(value, "=")
	if !found {
		return false
	}
	switch strings.ToUpper(key) {
	case "CUA_DRIVER_PERMISSION_MODE":
		return setting == "standard"
	case "CUA_DRIVER_RS_TELEMETRY_ENABLED":
		return setting == "false" || setting == "0"
	default:
		return slices.Contains([]string{"PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT",
			"WINDIR", "TEMP", "TMP", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"}, strings.ToUpper(key))
	}
}

func (c *Client) Call(ctx context.Context, tool string, input []byte) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := validateInput(tool, input); err != nil {
		return nil, false, err
	}
	if !filepath.IsAbs(c.binary) {
		return nil, false, ErrCall
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.binary, "call", tool)
	cmd.WaitDelay = time.Second
	cmd.Env = slices.Clone(c.env)
	cmd.Stdin = bytes.NewReader(input)
	output := &capture{}
	cmd.Stdout = stream{capture: output, keep: true}
	cmd.Stderr = stream{capture: output}
	if err := cmd.Start(); err != nil {
		return nil, false, ErrCall
	}
	data, err := output.result(ctx, cmd.Wait())
	return data, true, err
}

func validateInput(tool string, input []byte) error {
	if !slices.Contains([]string{"list_apps", "list_windows", "launch_app", "bring_to_front", "get_window_state",
		"click", "type_text", "press_key", "hotkey", "scroll", "start_session", "end_session"}, tool) {
		return ErrTool
	}
	var args map[string]json.RawMessage
	if len(input) > outputLimit || json.Unmarshal(input, &args) != nil || args == nil {
		return ErrInput
	}
	if tool == "launch_app" && (args["additional_arguments"] != nil || args["webkit_inspector_port"] != nil) {
		return ErrInput
	}
	return validateTarget(tool, args)
}

func validateTarget(tool string, args map[string]json.RawMessage) error {
	if scope, found := args["scope"]; found && string(scope) != `"window"` {
		return ErrInput
	}
	if args["screenshot_out_file"] != nil || args["debug_image_out"] != nil {
		return ErrInput
	}
	switch tool {
	case "get_window_state", "bring_to_front":
		if !positiveField(args["pid"]) || !positiveField(args["window_id"]) || args["target"] != nil {
			return ErrInput
		}
	case "click", "type_text", "press_key", "hotkey", "scroll":
		return validateActionTarget(args)
	default:
		if args["target"] != nil {
			return ErrInput
		}
	}
	return nil
}

func positiveField(raw json.RawMessage) bool {
	var value int64
	return json.Unmarshal(raw, &value) == nil && value > 0
}

func validateActionTarget(args map[string]json.RawMessage) error {
	var target struct {
		Kind   string `json:"kind"`
		PID    int64  `json:"pid"`
		Window int64  `json:"window_id"`
	}
	if json.Unmarshal(args["target"], &target) != nil || target.Kind != "window" ||
		target.PID <= 0 || target.Window <= 0 {
		return ErrInput
	}
	for field, expected := range map[string]int64{"pid": target.PID, "window_id": target.Window} {
		if raw, found := args[field]; found {
			var actual int64
			if json.Unmarshal(raw, &actual) != nil || actual != expected {
				return ErrInput
			}
		}
	}
	return nil
}

func (c *capture) result(ctx context.Context, waitErr error) ([]byte, error) {
	if c.exceeded {
		return nil, ErrLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	body := bytes.TrimSpace(c.body.Bytes())
	if len(body) == 0 || (body[0] != '{' && body[0] != '[') || !json.Valid(body) {
		return nil, ErrOutput
	}
	var normalized bytes.Buffer
	if compactErr := json.Compact(&normalized, body); compactErr != nil {
		return nil, ErrOutput
	}
	if waitErr != nil {
		return normalized.Bytes(), ErrCall
	}
	return normalized.Bytes(), nil
}

type capture struct {
	mu       sync.Mutex
	body     bytes.Buffer
	count    int
	exceeded bool
}

type stream struct {
	capture *capture
	keep    bool
}

func (s stream) Write(data []byte) (int, error) {
	c := s.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) > outputLimit-c.count {
		c.exceeded = true
		return 0, ErrLimit
	}
	c.count += len(data)
	if s.keep {
		return c.body.Write(data)
	}
	return len(data), nil
}
