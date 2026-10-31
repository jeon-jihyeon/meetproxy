package github

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Reaction contents GitHub takes
var Reactions = []string{"+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes"}

var ErrNotComment = errors.New("not a GitHub comment link")

// The review comment of a link sits on another pull request than the link's number
var ErrOtherPull = errors.New("the review comment sits on another pull request")

// GitHub deletes only a pending review, so a submitted one keeps its place
var ErrReviewStays = errors.New("a submitted review can't be deleted; retract it with text to replace its body")

// Posts body as a reply in the review thread of the link or as a comment on the issue or pull request
// A reply goes to the root of the review thread since GitHub takes no reply to a reply
// Returns the link of the new comment
func (c Client) Post(l messageurl.Link, body string) (string, error) {
	b, n := base(l.Repo), strconv.Itoa(l.Number)
	path := b + "/issues/" + n + "/comments"
	if l.Discussion != "" {
		x, on, err := c.review(l)
		if err != nil {
			return "", err
		}
		if on != l.Number {
			return "", ErrOtherPull
		}
		root := x.Root()
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

// Submits a review of the pull request on commit with event COMMENT or APPROVE
// commit pins it so a push after the head check never takes it
// Returns the link of the new review
func (c Client) Review(repo string, number int, commit, event, body string) (ReviewInfo, error) {
	path := base(repo) + "/pulls/" + strconv.Itoa(number) + "/reviews"
	out, err := c.send("POST", path, map[string]string{"commit_id": commit, "event": event, "body": body})
	if err != nil {
		return ReviewInfo{}, err
	}
	var posted ReviewInfo
	if err := json.Unmarshal(out, &posted); err != nil || posted.HTMLURL == "" {
		return ReviewInfo{}, fmt.Errorf("gh api %s: no link in the answer", path)
	}
	return posted, nil
}

// The head commit of the pull request the link names
func (c Client) Head(repo string, number int) (PullHead, error) {
	var pull struct {
		Head struct {
			Sha string `json:"sha"`
		} `json:"head"`
	}
	path := base(repo) + "/pulls/" + strconv.Itoa(number)
	if err := c.Get(path, &pull); err != nil {
		return PullHead{}, err
	}
	if pull.Head.Sha == "" {
		return PullHead{}, fmt.Errorf("gh api %s: no head commit", path)
	}
	return PullHead{SHA: pull.Head.Sha}, nil
}

// The API path of the comment the link points at, or of the issue or pull request itself
func itemPath(l messageurl.Link) string {
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
func (c Client) React(l messageurl.Link, content string) error {
	if !slices.Contains(Reactions, content) {
		return fmt.Errorf("reaction must be one of %v, not %q", Reactions, content)
	}
	_, err := c.run([]string{"api", "-X", "POST", itemPath(l) + "/reactions", "-f", "content=" + content}, nil)
	return err
}

// Only a comment can be edited or deleted so a link to the issue itself is refused
func commentPath(l messageurl.Link) (string, error) {
	if l.Discussion == "" && l.Comment == "" {
		return "", ErrNotComment
	}
	return itemPath(l), nil
}

func (c Client) Edit(l messageurl.Link, body string) error {
	if l.Review != "" {
		_, err := c.send("PUT", base(l.Repo)+"/pulls/"+strconv.Itoa(l.Number)+"/reviews/"+l.Review, map[string]string{"body": body})
		return err
	}
	path, err := commentPath(l)
	if err != nil {
		return err
	}
	_, err = c.send("PATCH", path, map[string]string{"body": body})
	return err
}

func (c Client) Delete(l messageurl.Link) error {
	if l.Review != "" {
		return ErrReviewStays
	}
	path, err := commentPath(l)
	if err != nil {
		return err
	}
	_, err = c.run([]string{"api", "-X", "DELETE", path}, nil)
	return err
}
