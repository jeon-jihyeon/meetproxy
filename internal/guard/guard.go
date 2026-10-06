// Package guard decides where a tool call posts and whether an open relay lets it
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

var ErrUnknownDest = errors.New("posting to an unknown destination, name the repository or channel")

func unknown(why string) error { return fmt.Errorf("%w: %s", ErrUnknownDest, why) }

var (
	slackReads  = regexp.MustCompile(`_(?:read|search|list|get)_|_draft$`)
	githubReads = regexp.MustCompile(`__(?:get|list|search)_`)
)

// Where a tool call posts
// 1. Nothing and no error: the call does not post
// 2. ErrUnknownDest: the call posts or may post somewhere that cannot be told
// Fails closed so an unknown posting tool or command is never let through silently
func Destinations(tool string, input json.RawMessage) ([]dest.Location, error) {
	name := strings.ToLower(tool)
	switch {
	case tool == "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, err
		}
		return bash(in.Command)
	case strings.Contains(name, "slack"):
		return slackPost(name, input)
	case strings.HasPrefix(name, "mcp__") && strings.Contains(name, "github"):
		return githubPost(name, input)
	}
	return nil, nil
}

// Reason to deny posting to locs while a relay is open and empty when every location passes
// 1. The origin covers its own thread or channel
// 2. The target covers only the pull request or issue it names
// 3. Anything else needs an allow list entry
func Decide(locs []dest.Location, origin, target dest.Location, allowed func(dest.Location) (bool, error)) (string, error) {
	for _, l := range locs {
		if origin.Covers(l) || target.Covers(l) {
			continue
		}
		ok, err := allowed(l)
		if err != nil {
			return "", err
		}
		if !ok {
			return l.String() + " is not allowed", nil
		}
	}
	return "", nil
}

// Reads and drafts do not post and every other Slack tool posts to its channel
func slackPost(name string, input json.RawMessage) ([]dest.Location, error) {
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
	return []dest.Location{loc}, nil
}

// Reads do not post and every other GitHub tool posts to its repository and number
func githubPost(name string, input json.RawMessage) ([]dest.Location, error) {
	if githubReads.MatchString(name) {
		return nil, nil
	}
	var in struct {
		Owner       string      `json:"owner"`
		Repo        string      `json:"repo"`
		IssueNumber json.Number `json:"issue_number"`
		PullNumber  json.Number `json:"pull_number"`
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
	return []dest.Location{loc}, nil
}
