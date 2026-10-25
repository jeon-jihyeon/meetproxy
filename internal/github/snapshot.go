package github

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"fmt"
	"strconv"
	"time"
)

func (c Client) Snapshot(l messageurl.Link) ([]Comment, error) {
	b, n := base(l.Repo), strconv.Itoa(l.Number)
	if l.Discussion != "" {
		all, err := c.Comments(b+"/pulls/"+n+"/comments", time.Time{})
		if err != nil {
			return nil, err
		}
		replies, root, err := c.inThread(l, all)
		if err != nil {
			return nil, err
		}
		for _, item := range all {
			if item.Id == root {
				return append([]Comment{item}, replies...), nil
			}
		}
		return nil, ErrNotComment
	}
	var root Comment
	if err := c.Get(b+"/issues/"+n, &root); err != nil {
		return nil, err
	}
	comments, err := c.Comments(b+"/issues/"+n+"/comments", time.Time{})
	if err != nil {
		return nil, err
	}
	if len(comments) < root.Comments {
		return nil, fmt.Errorf("thread snapshot is incomplete: read %d of %d comments", len(comments), root.Comments)
	}
	return append([]Comment{root}, comments...), nil
}
