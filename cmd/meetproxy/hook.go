package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/guard"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type hookInput struct {
	SessionId string          `json:"session_id"`
	Event     string          `json:"hook_event_name"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// Larger input such as a huge edit is denied rather than read into memory
const maxHookInput = 4 << 20

// Session ids name state files so any other shape could reach outside the data directory
var sessionId = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// The event each hook runs on
var hookEvents = map[string]string{"guard": "PreToolUse", "stop": "Stop", "end": "SessionEnd"}

// Input past the limit is not parsed and keeps only the session id it starts with
func readHook(in io.Reader) (hookInput, error) {
	var h hookInput
	b, err := io.ReadAll(io.LimitReader(in, maxHookInput+1))
	switch {
	case err != nil:
		return h, err
	case len(b) > maxHookInput:
		return hookInput{SessionId: leadingSession(b)}, errTooLarge
	}
	return h, json.Unmarshal(b, &h)
}

// The session id when it comes before every other value as Claude Code writes it
func leadingSession(b []byte) string {
	d := json.NewDecoder(bytes.NewReader(b))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	var pair struct{ key, value string }
	if t, err := d.Token(); err == nil {
		pair.key, _ = t.(string)
	}
	if t, err := d.Token(); err == nil {
		pair.value, _ = t.(string)
	}
	if pair.key != "session_id" {
		return ""
	}
	return pair.value
}

// Checked before the session id names any file
// An event name is checked only when Claude Code gives one
func (h hookInput) check(name string) error {
	if h.Event != "" && h.Event != hookEvents[name] {
		return fmt.Errorf("the %s hook got a %s event", name, h.Event)
	}
	if h.SessionId != "" && !sessionId.MatchString(h.SessionId) {
		return errBadSession
	}
	return nil
}

// A failure is kept in health.json so status shows it since the hook itself exits 0
func runHook(data string, args []string, now time.Time, in io.Reader, out io.Writer) error {
	name := strings.Join(args, " ")
	err := safeHook(data, name, now, in, out)
	if err != nil && data != "" {
		_ = recordHookFailure(data, name, err, now)
	}
	if name == "guard" && err != nil {
		// Fail closed since this is the safety check
		return deny(out, "meetproxy: posting check failed "+err.Error())
	}
	return err
}

// A panic becomes an error so the guard denies instead of letting Claude Code read a crash
func safeHook(data, name string, now time.Time, in io.Reader, out io.Writer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return hook(data, name, now, in, out)
}

func hook(data, name string, now time.Time, in io.Reader, out io.Writer) error {
	switch name {
	case "guard":
		return guardPost(data, now, in, out)
	case "stop":
		return endTurn(data, now, in)
	case "end":
		return endSession(data, now, in)
	default:
		return fmt.Errorf("unknown hook %v", name)
	}
}

var (
	errNoData     = errors.New("CLAUDE_PLUGIN_DATA is not set")
	errNoSession  = errors.New("the hook input names no session")
	errBadSession = errors.New("the hook input names a session id of an unknown shape")
	errTooLarge   = errors.New("the hook input is over 4 MiB")
)

// Enforced by a hook because request text is untrusted and no person reviews the post
// Non posting calls return before any state is read so broken state never blocks them
func guardPost(data string, now time.Time, in io.Reader, out io.Writer) error {
	h, err := readHook(in)
	if errors.Is(err, errTooLarge) {
		return tooLarge(data, h.SessionId, now)
	}
	if err != nil {
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
	if err := h.check("guard"); err != nil {
		return err
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

// Input too large to read passes only for a session that handles nothing
func tooLarge(data, session string, now time.Time) error {
	if data == "" || !sessionId.MatchString(session) {
		return errTooLarge
	}
	_, open, err := scopeOf(data, session, now)
	if err != nil || open {
		return errors.Join(errTooLarge, err)
	}
	return nil
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
	it, err := inbox.New(data).ByOrigin(origin)
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
	h, err := readHook(in)
	if err == nil {
		err = h.check("stop")
	}
	if err != nil || h.SessionId == "" {
		return err
	}
	if err := relay.New(data).EndTurn(h.SessionId); err != nil {
		return err
	}
	return releaseScope(data, h.SessionId, now)
}

// A session that ends gives its takes back so they open again at once and its scope ends with it
func endSession(data string, now time.Time, in io.Reader) error {
	if data == "" {
		return errNoData
	}
	h, err := readHook(in)
	if err == nil {
		err = h.check("end")
	}
	if err != nil || h.SessionId == "" {
		return err
	}
	relays := relay.New(data)
	if _, err := relays.Close(h.SessionId, now); err != nil && !errors.Is(err, relay.ErrNoOpen) {
		return err
	}
	if _, err := inbox.New(data).Release(h.SessionId, now); err != nil {
		return err
	}
	if err := relays.EndTurn(h.SessionId); err != nil {
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
