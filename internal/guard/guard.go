// Package guard decides where a tool call posts and whether an open relay lets it
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

var (
	ErrUnknownDest = errors.New("posting to an unknown destination, name the repository or channel")
	ErrSettings    = errors.New("changes meetproxy settings, ask the user to run it")
)

func unknown(why string) error { return fmt.Errorf("%w: %s", ErrUnknownDest, why) }

// One post of a tool call
type Post struct {
	At  dest.Location
	Act string
}

const (
	ActComment = ""        // a comment, a message or any other write
	ActApprove = "approve" // an approving review
	ActMerge   = "merge"
	ActClose   = "close"  // closing or archiving
	ActDelete  = "delete" // deleting anything
)

var (
	slackReads  = regexp.MustCompile(`_(?:read|search|list|get)_|_draft$`)
	githubReads = regexp.MustCompile(`__(?:get|list|search)_`)
	// The tool part of an MCP tool name that only reads
	toolReads = regexp.MustCompile(`(?i)__(?:get|list|search|read|fetch|find|query|view|lookup)`)
	// Words in an MCP tool name that may write somewhere the guard cannot tell
	toolWrites = regexp.MustCompile(`(?i)(?:post|send|comment|create|update|save|merge|delete|approve|reply|publish|share|submit|upload|schedule|add|remove|edit|write)`)
)

// Where a tool call posts
// 1. Nothing and no error: the call does not post
// 2. ErrUnknownDest: the call posts or may post somewhere that cannot be told
// 3. ErrSettings: the call changes what meetproxy takes or where it may post
// Fails closed so an unknown posting tool or command is never let through silently
// data is the plugin data directory whose files a command may not edit and empty when unknown
func Destinations(tool string, input json.RawMessage, data string) ([]Post, error) {
	name := strings.ToLower(tool)
	switch {
	case tool == "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, err
		}
		return bash(in.Command, data)
	case tool == "Write" || tool == "Edit" || tool == "NotebookEdit":
		return nil, fileEdit(input, data)
	// Its scripts are not read so every call counts as posting somewhere unknown
	case tool == "PowerShell":
		return nil, unknown("a PowerShell call the guard cannot read")
	// The post tool makes the same check before it sends
	case strings.HasPrefix(name, "mcp__meetproxy__"):
		return nil, nil
	case strings.Contains(name, "slack"):
		return slackPost(name, input)
	case strings.HasPrefix(name, "mcp__") && strings.Contains(name, "github"):
		return githubPost(name, input)
	case strings.HasPrefix(name, "mcp__") && toolWrites.MatchString(name) && !toolReads.MatchString(name):
		return nil, unknown("a tool that may post " + tool)
	}
	return nil, nil
}

// A file tool writing in the data directory changes what meetproxy takes or where it may post
// Any other path returns at once so editing code never waits on state
func fileEdit(input json.RawMessage, data string) error {
	if data == "" {
		return nil
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	data = filepath.Clean(data)
	for _, p := range []string{in.FilePath, in.NotebookPath} {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if p == data || strings.HasPrefix(p, data+string(filepath.Separator)) {
			return fmt.Errorf("a file edit in the meetproxy data directory %w", ErrSettings)
		}
	}
	return nil
}

// Reason to deny posts while a relay is open and empty when every post passes
// 1. Merging, closing and deleting are left to the user
// 2. Approving needs a delegation that lets the review approve
// 3. The origin covers its own thread or channel
// 4. The target covers only the pull request or issue it names
// 5. Anything else needs an allow list entry
func Decide(posts []Post, origin, target dest.Location, mayApprove bool, allowed func(dest.Location) (bool, error)) (string, error) {
	for _, p := range posts {
		switch {
		case p.Act == ActMerge || p.Act == ActClose || p.Act == ActDelete:
			return "a " + p.Act + " of " + p.At.String() + " is left to the user", nil
		case p.Act == ActApprove && !mayApprove:
			return "approving " + p.At.String() + " is not delegated", nil
		case origin.Covers(p.At) || target.Covers(p.At):
			continue
		}
		ok, err := allowed(p.At)
		if err != nil {
			return "", err
		}
		if !ok {
			return p.At.String() + " is not allowed", nil
		}
	}
	return "", nil
}

func postsAt(locs []dest.Location, act string) []Post {
	out := make([]Post, 0, len(locs))
	for _, l := range locs {
		out = append(out, Post{At: l, Act: act})
	}
	return out
}

// Reads and drafts do not post and every other Slack tool posts to its channel
func slackPost(name string, input json.RawMessage) ([]Post, error) {
	if slackReads.MatchString(name) {
		return nil, nil
	}
	var in struct {
		ChannelId string `json:"channel_id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	loc, ok := dest.Parse(dest.Slack + ":" + in.ChannelId)
	if !ok {
		return nil, unknown("a Slack post without a channel")
	}
	return []Post{{At: loc}}, nil
}

// Reads do not post and every other GitHub tool posts to its repository and number
func githubPost(name string, input json.RawMessage) ([]Post, error) {
	if githubReads.MatchString(name) {
		return nil, nil
	}
	var in struct {
		Owner       string      `json:"owner"`
		Repo        string      `json:"repo"`
		IssueNumber json.Number `json:"issue_number"`
		PullNumber  json.Number `json:"pull_number"`
		Event       string      `json:"event"`
		State       string      `json:"state"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, err
	}
	loc, ok := dest.Parse(dest.GitHub + ":" + in.Owner + "/" + in.Repo)
	if !ok {
		return nil, unknown("a GitHub post without an owner and a repository")
	}
	n, err := in.IssueNumber.Int64()
	if in.IssueNumber == "" {
		n, err = in.PullNumber.Int64()
	}
	if err == nil {
		loc.Number = int(n)
	}
	act := ActComment
	switch {
	case strings.Contains(name, "merge"):
		act = ActMerge
	case strings.Contains(name, "delete"):
		act = ActDelete
	case strings.EqualFold(in.Event, "APPROVE"):
		act = ActApprove
	case strings.EqualFold(in.State, "closed"):
		act = ActClose
	}
	return []Post{{At: loc, Act: act}}, nil
}
