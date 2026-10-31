package github

import "strconv"

type ReviewInfo struct {
	HTMLURL  string `json:"html_url"`
	CommitID string `json:"commit_id"`
	State    string `json:"state"`
}

type PullHead struct {
	SHA string
}

func (c Client) ReviewInfo(repo string, number int, id string) (ReviewInfo, error) {
	var result ReviewInfo
	err := c.Get(base(repo)+"/pulls/"+strconv.Itoa(number)+"/reviews/"+id, &result)
	return result, err
}

func (c Client) PullNumber(repo, discussion string) (int, error) {
	var result struct {
		PullURL string `json:"pull_request_url"`
	}
	if err := c.Get(base(repo)+"/pulls/comments/"+discussion, &result); err != nil {
		return 0, err
	}
	return pullNumber(result.PullURL)
}

func (c Client) CommentInfo(repo, id string, discussion bool) (Comment, error) {
	path := base(repo) + "/issues/comments/" + id
	if discussion {
		path = base(repo) + "/pulls/comments/" + id
	}
	var result Comment
	err := c.Get(path, &result)
	return result, err
}
