package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type hookInput struct {
	SessionId string `json:"session_id"`
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		Command   string `json:"command"`
		ChannelId string `json:"channel_id"`
	} `json:"tool_input"`
}

var errUnknownDest = errors.New("posting to an unknown destination, name the repository or channel")

func runHook(data string, args []string, in io.Reader, out io.Writer) error {
	switch strings.Join(args, " ") {
	case "path":
		return observePath(data, in)
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

func decodeHook(data string, in io.Reader) (hookInput, error) {
	var h hookInput
	if data == "" {
		return h, errors.New("CLAUDE_PLUGIN_DATA is not set")
	}
	return h, json.NewDecoder(in).Decode(&h)
}

func observePath(data string, in io.Reader) error {
	h, err := decodeHook(data, in)
	if err != nil {
		return err
	}
	target := h.ToolInput.FilePath
	if target == "" {
		target = h.ToolInput.Path
	}
	if target == "" || h.SessionId == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(h.Cwd, target)
	}
	p, ok := locmap.Resolve(target)
	if !ok {
		return nil
	}
	return relay.New(data).Observe(h.SessionId, p)
}

// Enforced by a hook because request text is untrusted and no person reviews the post
func guardPost(data string, in io.Reader, out io.Writer) error {
	h, err := decodeHook(data, in)
	if err != nil {
		return err
	}
	if h.SessionId == "" {
		return nil
	}
	r, err := relay.New(data).Current(h.SessionId)
	if errors.Is(err, relay.ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	locs, err := postTargets(h)
	if errors.Is(err, errUnknownDest) {
		return deny(out, fmt.Sprintf("meetproxy: relay %s is open, %s", r.Id, err))
	}
	allow := dest.New(data)
	for _, loc := range locs {
		allowed, err := allow.Allowed(loc)
		if err != nil {
			return err
		}
		if !allowed {
			return deny(out, fmt.Sprintf(
				"meetproxy: relay %s is open, %s is not allowed. Run meetproxy close if this post is unrelated", r.Id, loc))
		}
	}
	return nil
}

var (
	shellSep = regexp.MustCompile(`&&|\|\||;|\||\n`)
	ghRepo   = regexp.MustCompile(`(?:repos/|github\.com/|--repo[ =]|-R\s*)([\w.-]+)/([\w.-]+)`)
	ghWrite  = regexp.MustCompile(
		`\bgh\s+(?:pr\s+(?:comment|review)|issue\s+comment)\b` +
			`|\bgh\s+api\b.*(?:\s-[fF]|--field|--raw-field|--input|(?:-X|--method)[\s=]*(?:POST|PATCH|PUT))`)
	directAPI = regexp.MustCompile(`slack\.com/api|hooks\.slack\.com|api\.github\.com`)
)

// Returns nothing for a non-posting call and errUnknownDest when the destination cannot be told
func postTargets(h hookInput) ([]string, error) {
	if strings.Contains(strings.ToLower(h.ToolName), "slack") {
		if !strings.HasSuffix(h.ToolName, "send_message") && !strings.HasSuffix(h.ToolName, "schedule_message") {
			return nil, nil
		}
		if h.ToolInput.ChannelId == "" {
			return nil, errUnknownDest
		}
		return []string{"slack:" + h.ToolInput.ChannelId}, nil
	}
	if h.ToolName != "Bash" {
		return nil, nil
	}
	// Splitting on separators inside quotes can only cause extra denials
	var locs []string
	for _, seg := range shellSep.Split(h.ToolInput.Command, -1) {
		if ghWrite.MatchString(seg) {
			m := ghRepo.FindStringSubmatch(seg)
			if m == nil {
				return nil, errUnknownDest
			}
			locs = append(locs, "github:"+m[1]+"/"+m[2])
			continue
		}
		if directAPI.MatchString(seg) {
			return nil, errUnknownDest
		}
	}
	return locs, nil
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
