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

// A failure is kept in health.json so status shows it since the hook itself exits 0
func runHook(data string, args []string, now time.Time, in io.Reader, out io.Writer) error {
	name := strings.Join(args, " ")
	err := hook(data, name, now, in, out)
	if err != nil && data != "" {
		_ = recordHookFailure(data, name, err, now)
	}
	if name == "guard" && err != nil {
		// Fail closed since this is the safety check
		return deny(out, "meetproxy: posting check failed "+err.Error())
	}
	return err
}

func hook(data, name string, now time.Time, in io.Reader, out io.Writer) error {
	switch name {
	case "path":
		return observePath(data, now, in)
	case "start":
		return noticeWaiting(data, now, in, out)
	case "guard":
		return guardPost(data, now, in, out)
	case "stop":
		return endTurn(data, now, in)
	default:
		return fmt.Errorf("unknown hook %v", name)
	}
}

var (
	errNoData    = errors.New("CLAUDE_PLUGIN_DATA is not set")
	errNoSession = errors.New("the hook input names no session")
)

// A path read while a request is handled also tells the inbox its session still works on it
func observePath(data string, now time.Time, in io.Reader) error {
	if data == "" {
		return errNoData
	}
	// Best effort since without the directory the launcher only skips nothing
	_ = ensureScopeDir(data, now)
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
	if err := heartbeat(data, h.SessionId, now); err != nil {
		return err
	}
	p, ok := locmap.ResolveIn(target, h.Cwd)
	if !ok {
		return nil
	}
	return relay.New(data).Observe(h.SessionId, p)
}

// Renews the take whose relay the session has open
func heartbeat(data, session string, now time.Time) error {
	r, err := relay.New(data).Current(session)
	if errors.Is(err, relay.ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	it, err := inbox.New(data).Get(inbox.IdOf(r.Origin))
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		return nil
	case err != nil:
		return err
	case it.Status != inbox.StatusTaken || it.SessionId != session:
		return nil
	}
	return inbox.New(data).Touch(session, now)
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
// Non posting calls return before any state is read so broken state never blocks them
func guardPost(data string, now time.Time, in io.Reader, out io.Writer) error {
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	posts, destErr := guard.Destinations(h.ToolName, h.ToolInput, data)
	if destErr == nil && len(posts) == 0 {
		return nil
	}
	if data == "" {
		return errNoData
	}
	_ = ensureScopeDir(data, now)
	// Claude Code always names the session so a post without one fails closed rather than skip the scope check
	if h.SessionId == "" {
		return errNoSession
	}
	sc, open, err := scopeOf(data, h.SessionId, now)
	if err != nil || !open {
		return err
	}
	var reason string
	switch {
	case errors.Is(destErr, guard.ErrUnknownDest) || errors.Is(destErr, guard.ErrSettings):
		reason = destErr.Error()
	case destErr != nil:
		return destErr
	default:
		if reason, err = denial(data, posts, sc); err != nil {
			return err
		}
	}
	if reason == "" {
		return nil
	}
	// No way around the denial is named since the request text may be steering the session
	return deny(out, "meetproxy: a request is being handled, "+reason+". Leave this post to the user")
}

// The request a session handles and what its posts may do
type scope struct {
	origin, target string
	mayApprove     bool
}

// The scope of the session and whether it handles anything
// 1. Its open relay
// 2. A request it took and has not settled
// 3. A relay or request it settled in the turn still running
// So neither a take before the relay opens nor a close before the turn ends lets a post go anywhere
func scopeOf(data, session string, now time.Time) (scope, bool, error) {
	relays := relay.New(data)
	r, err := relays.Current(session)
	switch {
	case err == nil:
		return scopeFor(data, r.Origin, r.Target)
	case !errors.Is(err, relay.ErrNoOpen):
		return scope{}, false, err
	}
	it, taken, err := inbox.New(data).TakenBy(session, now)
	if err != nil || taken {
		return scope{it.Link, it.Target, it.MayApprove}, taken, err
	}
	r, ended, err := relays.Ended(session, now)
	if err != nil || !ended {
		return scope{}, false, err
	}
	return scopeFor(data, r.Origin, r.Target)
}

// A relay the user opened by hand comes from no request so its review approves as the user asks
func scopeFor(data, origin, target string) (scope, bool, error) {
	it, err := inbox.New(data).Get(inbox.IdOf(origin))
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		return scope{origin, target, true}, true, nil
	case err != nil:
		return scope{}, false, err
	}
	return scope{origin, target, it.MayApprove}, true, nil
}

// The scope a close or a settle kept ends with the turn
// The marker goes with it once nothing else keeps a scope
func endTurn(data string, now time.Time, in io.Reader) error {
	if data == "" {
		return errNoData
	}
	var h hookInput
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return err
	}
	if h.SessionId == "" {
		return nil
	}
	if err := relay.New(data).EndTurn(h.SessionId); err != nil {
		return err
	}
	return releaseScope(data, h.SessionId, now)
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
