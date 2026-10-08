package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

// Reaction contents GitHub takes
var Reactions = []string{"+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes"}

var ErrNotComment = errors.New("not a GitHub comment link")

// Posts body as a reply in the review thread of the link or as a comment on the issue or pull request
// A reply goes to the root of the review thread since GitHub takes no reply to a reply
// Returns the link of the new comment
func (c Client) Post(l dest.Link, body string) (string, error) {
	b, n := base(l.Repo), strconv.Itoa(l.Number)
	path := b + "/issues/" + n + "/comments"
	if l.Discussion != "" {
		root, err := c.root(l, nil)
		if err != nil {
			return "", err
		}
		path = b + "/pulls/" + n + "/comments/" + strconv.FormatInt(root, 10) + "/replies"
	}
	out, err := c.send("POST", path, map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var posted Comment
	if err := json.Unmarshal(out, &posted); err != nil || posted.HTMLURL == "" {
		return "", fmt.Errorf("gh api %s: no link in the answer", path)
	}
	return posted.HTMLURL, nil
}

// The API path of the comment the link points at, or of the issue or pull request itself
func itemPath(l dest.Link) string {
	b := base(l.Repo)
	switch {
	case l.Discussion != "":
		return b + "/pulls/comments/" + l.Discussion
	case l.Comment != "":
		return b + "/issues/comments/" + l.Comment
	}
	return b + "/issues/" + strconv.Itoa(l.Number)
}

// One already there counts as added since GitHub answers it with the existing reaction
func (c Client) React(l dest.Link, content string) error {
	if !slices.Contains(Reactions, content) {
		return fmt.Errorf("reaction must be one of %v, not %q", Reactions, content)
	}
	_, err := c.run([]string{"api", "-X", "POST", itemPath(l) + "/reactions", "-f", "content=" + content}, nil)
	return err
}

// Only a comment can be edited or deleted so a link to the issue itself is refused
func commentPath(l dest.Link) (string, error) {
	if l.Discussion == "" && l.Comment == "" {
		return "", ErrNotComment
	}
	return itemPath(l), nil
}

func (c Client) Edit(l dest.Link, body string) error {
	path, err := commentPath(l)
	if err != nil {
		return err
	}
	_, err = c.send("PATCH", path, map[string]string{"body": body})
	return err
}

func (c Client) Delete(l dest.Link) error {
	path, err := commentPath(l)
	if err != nil {
		return err
	}
	_, err = c.run([]string{"api", "-X", "DELETE", path}, nil)
	return err
}
