package github

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type ReviewThread struct {
	Root int64
	Pull int
}

func (c Client) ReviewThread(repo, discussion string) (ReviewThread, error) {
	var result struct {
		Comment
		PullURL string `json:"pull_request_url"`
	}
	if err := c.Get(base(repo)+"/pulls/comments/"+discussion, &result); err != nil {
		return ReviewThread{}, err
	}
	number, err := pullNumber(result.PullURL)
	if err != nil {
		return ReviewThread{}, err
	}
	return ReviewThread{Root: result.Root(), Pull: number}, nil
}

func (c Client) PostComment(repo string, number int, root int64, body string) (Comment, error) {
	path := base(repo) + "/issues/" + strconv.Itoa(number) + "/comments"
	if root != 0 {
		path = base(repo) + "/pulls/" + strconv.Itoa(number) + "/comments/" + strconv.FormatInt(root, 10) + "/replies"
	}
	out, err := c.send("POST", path, map[string]string{"body": body})
	if err != nil {
		return Comment{}, err
	}
	var result Comment
	if json.Unmarshal(out, &result) != nil || result.HTMLURL == "" {
		return Comment{}, fmt.Errorf("gh api %s: no link in the answer", path)
	}
	return result, nil
}

func (c Client) EditComment(repo, id string, discussion bool, body string) error {
	path := base(repo) + "/issues/comments/" + id
	if discussion {
		path = base(repo) + "/pulls/comments/" + id
	}
	_, err := c.send("PATCH", path, map[string]string{"body": body})
	return err
}

func (c Client) DeleteComment(repo, id string, discussion bool) error {
	path := base(repo) + "/issues/comments/" + id
	if discussion {
		path = base(repo) + "/pulls/comments/" + id
	}
	_, err := c.run([]string{"api", "-X", "DELETE", path}, nil)
	return err
}

func (c Client) EditReview(repo string, number int, id, body string) error {
	path := base(repo) + "/pulls/" + strconv.Itoa(number) + "/reviews/" + id
	_, err := c.send("PUT", path, map[string]string{"body": body})
	return err
}
