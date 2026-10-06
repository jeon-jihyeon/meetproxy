package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/guard"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type hookInput struct {
	SessionId string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

func runHook(data string, args []string, now time.Time, in io.Reader, out io.Writer) error {
	switch strings.Join(args, " ") {
	case "path":
		return observePath(data, in)
	case "start":
		return noticeWaiting(data, now, in, out)
	case "guard":
		// Fail closed since this is the safety check
		if err := guardPost(data, in, out); err != nil {
			return deny(out, "meetproxy: posting check failed "+err.Error())
		}
		return nil
	default:
		return fmt.Errorf("unknown hook %v", args)
	}
}

var (
	errNoData    = errors.New("CLAUDE_PLUGIN_DATA is not set")
	errNoSession = errors.New("the hook input names no session")
)

func observePath(data string, in io.Reader) error {
	if data == "" {
		return errNoData
	}
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	if h.SessionId == "" || len(h.ToolInput) == 0 {
		return nil
	}
	var tool struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(h.ToolInput, &tool); err != nil {
		return err
	}
	target := tool.FilePath
	if target == "" {
		target = tool.Path
	}
	if target == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(h.Cwd, target)
	}
	p, ok := locmap.ResolveIn(target, h.Cwd)
	if !ok {
		return nil
	}
	return relay.New(data).Observe(h.SessionId, p)
}

// Names the requests waiting for the place this session starts in
// Only ids and links are shown since request text is untrusted
func noticeWaiting(data string, now time.Time, in io.Reader, out io.Writer) error {
	if data == "" {
		return errNoData
	}
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	if h.Cwd == "" {
		return nil
	}
	root, name := locmap.Place(h.Cwd)
	waiting, err := inbox.New(data).Waiting(now)
	if err != nil {
		return err
	}
	var items []inbox.Item
	for _, it := range waiting {
		if it.At(root, name) {
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		return nil
	}
	var b strings.Builder
	noun := "requests wait"
	if len(items) == 1 {
		noun = "request waits"
	}
	fmt.Fprintf(&b, "meetproxy: %d %s for %s. Tell the user, and run /meetproxy:handle <id> for the ones they ask for.", len(items), noun, name)
	for _, it := range items {
		fmt.Fprintf(&b, "\n%s %s", it.Id, it.Link)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "SessionStart",
			"additionalContext": b.String(),
		},
	})
}

// Enforced by a hook because request text is untrusted and no person reviews the post
// Non posting calls return before any relay state is read so broken state never blocks them
func guardPost(data string, in io.Reader, out io.Writer) error {
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	locs, destErr := guard.Destinations(h.ToolName, h.ToolInput)
	if destErr == nil && len(locs) == 0 {
		return nil
	}
	if data == "" {
		return errNoData
	}
	// Claude Code always names the session so a post without one fails closed rather than skip the relay check
	if h.SessionId == "" {
		return errNoSession
	}
	r, err := relay.New(data).Current(h.SessionId)
	if errors.Is(err, relay.ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	if errors.Is(destErr, guard.ErrUnknownDest) {
		return deny(out, fmt.Sprintf("meetproxy: relay %s is open, %s", r.Id, destErr))
	}
	if destErr != nil {
		return destErr
	}
	reason, err := denial(data, locs, r.Origin, r.Target)
	if err != nil {
		return err
	}
	if reason != "" {
		return deny(out, fmt.Sprintf("meetproxy: relay %s is open, %s. Run meetproxy close if this post is unrelated", r.Id, reason))
	}
	return nil
}

func deny(out io.Writer, reason string) error {
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}
