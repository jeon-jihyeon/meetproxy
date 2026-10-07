// Package slackapi calls the Slack Web API with the user's own token
package slackapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	Needed string
}

func (e *Error) Error() string {
	if e.Needed != "" {
		return fmt.Sprintf("slack %s: %s, the token needs %s", e.Method, e.Code, e.Needed)
	}
	return fmt.Sprintf("slack %s: %s", e.Method, e.Code)
}

var ErrBase = errors.New("a Slack API base other than slack.com must be a loopback address")

type Client struct {
	base  string
	token string
	http  *http.Client
	sleep func(time.Duration)
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

// One message as the API answers it
type Message struct {
	Ts       string `json:"ts"`
	User     string `json:"user"`
	BotId    string `json:"bot_id"`
	Username string `json:"username"`
	Text     string `json:"text"`
	// Set by search alone
	Permalink string `json:"permalink"`
	Channel   struct {
		Id string `json:"id"`
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

// Whether people outside the user's workspace can read the channel
// A channel shared within an organization counts too since its members may sit in another workspace
func (c Client) Shared(channel string) (bool, error) {
	var r struct {
		Channel struct {
			IsShared    bool `json:"is_shared"`
			IsExtShared bool `json:"is_ext_shared"`
		} `json:"channel"`
	}
	_, err := c.call("conversations.info", url.Values{"channel": {channel}}, &r)
	return r.Channel.IsShared || r.Channel.IsExtShared, err
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

// A member of a workspace as the trust check needs it
type User struct {
	Id   string `json:"id"`
	Team string `json:"team"`
	Name string `json:"name"`
	// Deactivated
	Deleted bool `json:"deleted"`
	// A guest, single or multi channel
	Restricted bool `json:"restricted"`
}

func (c Client) UserInfo(id string) (User, error) {
	var r struct {
		User struct {
			Id                string `json:"id"`
			TeamId            string `json:"team_id"`
			Name              string `json:"name"`
			RealName          string `json:"real_name"`
			Deleted           bool   `json:"deleted"`
			IsRestricted      bool   `json:"is_restricted"`
			IsUltraRestricted bool   `json:"is_ultra_restricted"`
		} `json:"user"`
	}
	if _, err := c.call("users.info", url.Values{"user": {id}}, &r); err != nil {
		return User{}, err
	}
	u := r.User
	name := u.RealName
	if name == "" {
		name = u.Name
	}
	return User{Id: u.Id, Team: u.TeamId, Name: name, Deleted: u.Deleted, Restricted: u.IsRestricted || u.IsUltraRestricted}, nil
}

// Posts the form to method and decodes the answer into v
// A rate limit is waited out once as Retry-After asks and fails the second time
func (c Client) call(method string, form url.Values, v any) (http.Header, error) {
	for try := 0; ; try++ {
		req, err := http.NewRequest(http.MethodPost, c.base+"/"+method, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			// The error names the URL and never the token
			return nil, err
		}
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			if try > 0 {
				return nil, &Error{Method: method, Code: "ratelimited"}
			}
			c.sleep(retryAfter(resp.Header.Get("Retry-After")))
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

func retryAfter(header string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || s < 1 {
		return time.Second
	}
	return min(time.Duration(s)*time.Second, maxRetryAfter)
}
