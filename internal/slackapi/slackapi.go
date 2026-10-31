// Package slackapi calls the Slack Web API with the user's own token
package slackapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const DefaultBase = "https://slack.com/api"

const (
	requestTimeout = 30 * time.Second
	// Longest Retry-After waited out once before a rate limit fails the call
	maxRetryAfter = time.Minute
	// Messages one page of search or history holds
	pageSize = 100
	// Pages read in one call
	// Far more than a busy channel gets in a day and still a bound on a runaway loop
	MaxPages = 50
)

// A Slack answer with ok false
type Error struct {
	Method string
	// Slack's error code such as missing_scope or invalid_auth
	Code string
	// The scope a missing_scope answer names
	Needed     string
	RetryAfter string
}

func (e *Error) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("slack %s: %s retry_after:%s", e.Method, e.Code, e.RetryAfter)
	}
	if e.Needed != "" {
		return fmt.Sprintf("slack %s: %s, the token needs %s", e.Method, e.Code, e.Needed)
	}
	return fmt.Sprintf("slack %s: %s", e.Method, e.Code)
}

var ErrBase = errors.New("a Slack API base other than slack.com must be a loopback address")

// The client's deadline passed before the request left, so Slack never got it
var ErrPastDeadline = errors.New("the deadline passed before the request was sent")

type Client struct {
	base  string
	token string
	http  *http.Client
	sleep func(time.Duration)
	// The wall time every call ends by, zero for none
	deadline time.Time
}

// The client whose calls end by deadline
// A Retry-After that leaves no full request time after the wait fails at once
func (c Client) Until(deadline time.Time) Client {
	c.deadline = deadline
	return c
}

func (c Client) context() (context.Context, context.CancelFunc) {
	if c.deadline.IsZero() {
		return context.WithCancel(context.Background())
	}
	return context.WithDeadline(context.Background(), c.deadline)
}

// An empty base is slack.com
// Any other base must be a loopback address so a changed environment never sends the token elsewhere
func New(base, token string) (Client, error) {
	if base == "" {
		base = DefaultBase
	} else if !loopback(base) {
		return Client{}, fmt.Errorf("%w: %q", ErrBase, base)
	}
	return Client{base: strings.TrimSuffix(base, "/"), token: token, http: &http.Client{Timeout: requestTimeout}, sleep: time.Sleep}, nil
}

func loopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// Who the token acts for
type Auth struct {
	User string `json:"user"`
	Team string `json:"team"`
	// Host of the workspace such as acme.slack.com
	Host string `json:"host"`
	// Scopes the token was granted
	Scopes []string `json:"scopes"`
}

func (c Client) AuthTest() (Auth, error) {
	var r struct {
		URL    string `json:"url"`
		UserId string `json:"user_id"`
		TeamId string `json:"team_id"`
	}
	h, err := c.call("auth.test", url.Values{}, &r)
	if err != nil {
		return Auth{}, err
	}
	a := Auth{User: r.UserId, Team: r.TeamId}
	if u, err := url.Parse(r.URL); err == nil {
		a.Host = u.Hostname()
	}
	for s := range strings.SplitSeq(h.Get("X-OAuth-Scopes"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.Scopes = append(a.Scopes, s)
		}
	}
	return a, nil
}

// Revokes the token, true once Slack says it is revoked
func (c Client) Revoke() (bool, error) {
	var r struct {
		Revoked bool `json:"revoked"`
	}
	_, err := c.call("auth.revoke", url.Values{}, &r)
	return r.Revoked, err
}

// One message as the API answers it
type Message struct {
	Subtype    string          `json:"subtype"`
	Ts         string          `json:"ts"`
	User       string          `json:"user"`
	BotId      string          `json:"bot_id"`
	Username   string          `json:"username"`
	Text       string          `json:"text"`
	Edited     json.RawMessage `json:"edited,omitempty"`
	Reactions  json.RawMessage `json:"reactions,omitempty"`
	ReplyCount int             `json:"reply_count,omitempty"`
	// ts of the thread a reply sits in
	ThreadTs string `json:"thread_ts"`
	// Set by search alone
	Permalink string `json:"permalink"`
	// Search sets the kind of the conversation
	Channel struct {
		Id     string `json:"id"`
		IsIm   bool   `json:"is_im"`
		IsMpim bool   `json:"is_mpim"`
	} `json:"channel"`
	BotProfile struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
}

// Messages that mention the user after the unix seconds after, oldest first
// 1. Search reads whole days so it asks from the day before after and filters by ts
// 2. Bot messages are left out as the connector leaves them out
func (c Client) SearchMentions(user, after string) ([]Message, error) {
	since, err := strconv.ParseFloat(after, 64)
	if err != nil {
		return nil, fmt.Errorf("after must be unix seconds %q", after)
	}
	day := time.Unix(int64(since), 0).UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	var out []Message
	for page := 1; page <= MaxPages; page++ {
		var r struct {
			Messages struct {
				Matches []Message `json:"matches"`
				Paging  struct {
					Pages int `json:"pages"`
				} `json:"paging"`
			} `json:"messages"`
		}
		form := url.Values{
			"query": {"<@" + user + "> after:" + day}, "sort": {"timestamp"}, "sort_dir": {"asc"},
			"count": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)},
		}
		if _, err := c.call("search.messages", form, &r); err != nil {
			return nil, err
		}
		for _, m := range r.Messages.Matches {
			if ts, err := strconv.ParseFloat(m.Ts, 64); err == nil && ts > since && m.User != "" {
				out = append(out, m)
			}
		}
		if page >= r.Messages.Paging.Pages {
			break
		}
	}
	return out, nil
}

// Messages of a channel after the ts oldest, newest first as the API answers
// Reports true when more than MaxPages pages were there so the oldest ones were left out
func (c Client) History(channel, oldest string) ([]Message, bool, error) {
	var out []Message
	cursor := ""
	for range MaxPages {
		var r struct {
			Messages []Message `json:"messages"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		form := url.Values{"channel": {channel}, "oldest": {oldest}, "limit": {strconv.Itoa(pageSize)}}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		if _, err := c.call("conversations.history", form, &r); err != nil {
			return nil, false, err
		}
		out = append(out, r.Messages...)
		if cursor = r.Meta.NextCursor; cursor == "" {
			return out, false, nil
		}
	}
	return out, true, nil
}

// The thread of ts after the ts oldest, the parent first
// An empty oldest reads from the parent
func (c Client) Replies(channel, ts, oldest string, limit int) ([]Message, error) {
	var r struct {
		Messages []Message `json:"messages"`
	}
	form := url.Values{"channel": {channel}, "ts": {ts}, "limit": {strconv.Itoa(limit)}}
	if oldest != "" {
		form.Set("oldest", oldest)
	}
	_, err := c.call("conversations.replies", form, &r)
	return r.Messages, err
}

// Posts text in the thread of thread or in the channel when thread is empty and returns the ts of the post
func (c Client) Post(channel, thread, text string) (string, error) {
	form := url.Values{"channel": {channel}, "text": {text}}
	if thread != "" {
		form.Set("thread_ts", thread)
	}
	var r struct {
		Ts string `json:"ts"`
	}
	_, err := c.call("chat.postMessage", form, &r)
	return r.Ts, err
}

// Replaces the text of a message the token's user posted
func (c Client) Update(channel, ts, text string) error {
	_, err := c.call("chat.update", url.Values{"channel": {channel}, "ts": {ts}, "text": {text}}, nil)
	return err
}

// Deletes a message the token's user posted
func (c Client) Delete(channel, ts string) error {
	_, err := c.call("chat.delete", url.Values{"channel": {channel}, "ts": {ts}}, nil)
	return err
}

// Adds a reaction to a message
// One already there counts as added so a retry never fails
func (c Client) React(channel, ts, name string) error {
	_, err := c.call("reactions.add", url.Values{"channel": {channel}, "timestamp": {ts}, "name": {name}}, nil)
	var e *Error
	if errors.As(err, &e) && e.Code == "already_reacted" {
		return nil
	}
	return err
}

// Ids of the user's direct and group direct conversations
// Every page is read within MaxPages since a conversation left out would lose its messages
func (c Client) DirectConversations() ([]string, error) {
	var ids []string
	cursor := ""
	for range MaxPages {
		var r struct {
			Channels []struct {
				Id string `json:"id"`
			} `json:"channels"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		form := url.Values{"types": {"im,mpim"}, "exclude_archived": {"true"}, "limit": {"200"}}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		if _, err := c.call("conversations.list", form, &r); err != nil {
			return nil, err
		}
		for _, ch := range r.Channels {
			ids = append(ids, ch.Id)
		}
		if cursor = r.Meta.NextCursor; cursor == "" {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("slack conversations.list: more than %d pages of direct conversations", MaxPages)
}

// A conversation as conversations.info and conversations.list answer it
type Conversation struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	IsIm bool   `json:"is_im"`
	// The other member of a direct conversation
	User string `json:"user"`
	// Names a channel had before its renames
	PreviousNames []string `json:"previous_names"`
}

func (c Client) ConversationInfo(id string) (Conversation, error) {
	var r struct {
		Channel Conversation `json:"channel"`
	}
	_, err := c.call("conversations.info", url.Values{"channel": {id}}, &r)
	return r.Channel, err
}

// Every conversation of the types the user is a member of, within MaxPages
func (c Client) Conversations(types string) ([]Conversation, error) {
	var out []Conversation
	cursor := ""
	for range MaxPages {
		var r struct {
			Channels []Conversation `json:"channels"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		form := url.Values{"types": {types}, "exclude_archived": {"true"}, "limit": {"200"}}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		if _, err := c.call("users.conversations", form, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Channels...)
		if cursor = r.Meta.NextCursor; cursor == "" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("slack users.conversations: more than %d pages", MaxPages)
}

// A member of the workspace with every name it goes by
type Member struct {
	Id    string
	Names []string
}

// Every member of the workspace that is not deleted, within MaxPages
// A list that runs past MaxPages fails since a name matched against part of it proves nothing
func (c Client) Members() ([]Member, error) {
	var out []Member
	cursor := ""
	for range MaxPages {
		var r struct {
			Members []struct {
				Id       string `json:"id"`
				Name     string `json:"name"`
				Deleted  bool   `json:"deleted"`
				RealName string `json:"real_name"`
				Profile  struct {
					DisplayName string `json:"display_name"`
					RealName    string `json:"real_name"`
				} `json:"profile"`
			} `json:"members"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		form := url.Values{"limit": {"200"}}
		if cursor != "" {
			form.Set("cursor", cursor)
		}
		if _, err := c.call("users.list", form, &r); err != nil {
			return nil, err
		}
		for _, u := range r.Members {
			if u.Deleted {
				continue
			}
			out = append(out, Member{Id: u.Id, Names: namesOf(u.Name, u.RealName, u.Profile.DisplayName, u.Profile.RealName)})
		}
		if cursor = r.Meta.NextCursor; cursor == "" {
			return out, nil
		}
	}
	return nil, fmt.Errorf("slack users.list: more than %d pages", MaxPages)
}

// The distinct non-empty names a user goes by
func namesOf(names ...string) []string {
	var out []string
	for _, n := range names {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// A user of any workspace by the name messages show
type User struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	// Every name it goes by as Members matches them
	Names   []string `json:"names,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
}

func (c Client) UserInfo(id string) (User, error) {
	var r struct {
		User struct {
			Id       string `json:"id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Deleted  bool   `json:"deleted"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if _, err := c.call("users.info", url.Values{"user": {id}}, &r); err != nil {
		return User{}, err
	}
	u := r.User
	return User{Id: u.Id, Name: cmp.Or(u.RealName, u.Name), Names: namesOf(u.Name, u.RealName, u.Profile.DisplayName, u.Profile.RealName), Deleted: u.Deleted}, nil
}

// Posts the form to method and decodes the answer into v
// A rate limit is waited out once as Retry-After asks and fails the second time
// The wait runs only while the retry still has its whole request time before the deadline
func (c Client) call(method string, form url.Values, v any) (http.Header, error) {
	for try := 0; ; try++ {
		resp, b, err := c.post(method, form)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			seconds, wait := retryAfter(resp.Header.Get("Retry-After"))
			if try > 0 || wait == 0 || !c.deadline.IsZero() && time.Until(c.deadline) < wait+requestTimeout {
				return nil, &Error{Method: method, Code: "ratelimited", RetryAfter: seconds}
			}
			c.sleep(wait)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
		}
		var env struct {
			Ok     bool   `json:"ok"`
			Error  string `json:"error"`
			Needed string `json:"needed"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return nil, fmt.Errorf("slack %s: %w", method, err)
		}
		if !env.Ok {
			return nil, &Error{Method: method, Code: env.Error, Needed: env.Needed}
		}
		if v != nil {
			if err := json.Unmarshal(b, v); err != nil {
				return nil, fmt.Errorf("slack %s: %w", method, err)
			}
		}
		return resp.Header, nil
	}
}

// One request and its whole answer, ended by the client's deadline
func (c Client) post(method string, form url.Values) (*http.Response, []byte, error) {
	ctx, cancel := c.context()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("slack %s: %w: %w", method, ErrPastDeadline, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		// The error names the URL and never the token
		return nil, nil, err
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, b, err
}

func retryAfter(header string) (string, time.Duration) {
	header = strings.TrimLeft(strings.TrimSpace(header), "0")
	if header == "" || strings.ContainsAny(header, "\r\n") {
		return "1", time.Second
	}
	for _, r := range header {
		if r < '0' || r > '9' {
			return "1", time.Second
		}
	}
	s, err := strconv.ParseInt(header, 10, 64)
	if err != nil || s > int64(maxRetryAfter/time.Second) {
		return header, 0
	}
	return header, time.Duration(s) * time.Second
}
