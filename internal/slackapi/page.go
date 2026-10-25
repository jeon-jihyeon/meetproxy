package slackapi

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

func (c Client) MentionPage(user, after, until, cursor string) ([]Message, string, error) {
	start, err := strconv.ParseFloat(after, 64)
	if err != nil {
		return nil, "", err
	}
	end, err := strconv.ParseFloat(until, 64)
	if err != nil {
		return nil, "", err
	}
	page := 1
	if cursor != "" {
		page, err = strconv.Atoi(cursor)
		if err != nil || page < 1 {
			return nil, "", fmt.Errorf("invalid search page")
		}
	}
	day := time.Unix(int64(start), 0).UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	last := time.Unix(int64(end), 0).UTC().AddDate(0, 0, 1).Format(time.DateOnly)
	form := url.Values{"query": {"<@" + user + "> after:" + day + " before:" + last}, "sort": {"timestamp"}, "sort_dir": {"asc"}, "count": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
	var r struct {
		Messages struct {
			Matches []Message `json:"matches"`
			Paging  struct {
				Pages int `json:"pages"`
			} `json:"paging"`
		} `json:"messages"`
	}
	if _, err := c.call("search.messages", form, &r); err != nil {
		return nil, "", err
	}
	next := ""
	if page < r.Messages.Paging.Pages {
		next = strconv.Itoa(page + 1)
	}
	return r.Messages.Matches, next, nil
}

func (c Client) HistoryPage(channel, after, until, cursor string) ([]Message, string, error) {
	form := url.Values{"channel": {channel}, "oldest": {after}, "latest": {until}, "limit": {strconv.Itoa(pageSize)}}
	return c.conversationPage("conversations.history", form, cursor)
}

func (c Client) ParentPage(channel, after, until, cursor string) ([]Message, string, bool, error) {
	form := url.Values{"channel": {channel}, "oldest": {after}, "latest": {until}, "limit": {strconv.Itoa(pageSize)}}
	r, err := c.readConversationPage("conversations.history", form, cursor)
	return r.Messages, r.Meta.Next, r.Limited, err
}

func (c Client) RepliesPage(channel, thread, after, until, cursor string) ([]Message, string, error) {
	form := url.Values{"channel": {channel}, "ts": {thread}, "oldest": {after}, "latest": {until}, "limit": {strconv.Itoa(pageSize)}}
	return c.conversationPage("conversations.replies", form, cursor)
}

func (c Client) conversationPage(method string, form url.Values, cursor string) ([]Message, string, error) {
	r, err := c.readConversationPage(method, form, cursor)
	return r.Messages, r.Meta.Next, err
}

type conversationPage struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"has_more"`
	Limited  bool      `json:"is_limited"`
	Meta     struct {
		Next string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (c Client) readConversationPage(method string, form url.Values, cursor string) (conversationPage, error) {
	if cursor != "" {
		form.Set("cursor", cursor)
	}
	var r conversationPage
	_, err := c.call(method, form, &r)
	if err == nil && r.HasMore && r.Meta.Next == "" {
		return r, fmt.Errorf("slack has more messages without a continuation cursor")
	}
	return r, err
}
