package github

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"strconv"
	"strings"
	"time"
)

const textChars = 4000

const (
	KindMention       = "mention"
	KindReviewRequest = "review-request"
	KindOwnPull       = "own-pr"
)

// A message in the shape the plugin mod reads from every source
type Message struct {
	Source string `json:"source"`
	// Empty for a reply in a watched thread
	Kind string `json:"kind,omitempty"`
	// The user wrote it
	Self   bool   `json:"self"`
	Author string `json:"author"`
	// The repository as owner and name joined by a slash
	Channel string `json:"channel"`
	From    string `json:"from"`
	// Unix seconds
	Ts   string `json:"ts"`
	Link string `json:"link"`
	Text string `json:"text"`
	// Key of the request the message belongs to
	// A review thread is one request named by its root comment
	Thread string `json:"thread"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Root comment id of the review thread, empty outside one
	Discussion string `json:"discussion,omitempty"`
	// How the author relates to the repository such as MEMBER or NONE
	Association string `json:"association,omitempty"`
}

func Ts(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func clip(s string) string {
	r := []rune(s)
	return string(r[:min(len(r), textChars)])
}

func base(repo string) string { return "repos/" + repo }

// The review thread a link sits in, the root comment of it
// The link of a reply names the reply so its root is looked up among cs first and asked of GitHub otherwise
func (c Client) root(l messageurl.Link, cs []Comment) (int64, error) {
	id, err := strconv.ParseInt(l.Discussion, 10, 64)
	if err != nil {
		return 0, err
	}
	for _, x := range cs {
		if x.Id == id {
			return x.Root(), nil
		}
	}
	var x Comment
	if err := c.Get(base(l.Repo)+"/pulls/comments/"+l.Discussion, &x); err != nil {
		return 0, err
	}
	return x.Root(), nil
}

// The replies in the review thread of the link among cs
func (c Client) inThread(l messageurl.Link, cs []Comment) ([]Comment, int64, error) {
	root, err := c.root(l, cs)
	if err != nil {
		return nil, 0, err
	}
	var out []Comment
	for _, x := range cs {
		if x.InReplyTo == root {
			out = append(out, x)
		}
	}
	return out, root, nil
}

func (c Client) Read(l messageurl.Link, limit int) (string, error) {
	limit = max(limit, 1)
	b, n := base(l.Repo), strconv.Itoa(l.Number)
	var cs []Comment
	switch {
	case l.Discussion != "":
		all, err := c.Comments(b+"/pulls/"+n+"/comments", time.Time{})
		if err != nil {
			return "", err
		}
		replies, root, err := c.inThread(l, all)
		if err != nil {
			return "", err
		}
		for _, x := range all {
			if x.Id == root {
				cs = append(cs, x)
			}
		}
		cs = append(cs, replies...)
	case l.Comment != "":
		var x Comment
		if err := c.Get(b+"/issues/comments/"+l.Comment, &x); err != nil {
			return "", err
		}
		cs = []Comment{x}
	default:
		var it Comment
		if err := c.Get(b+"/issues/"+n, &it); err != nil {
			return "", err
		}
		it.Body = it.Title + "\n" + it.Body
		rest, err := c.Comments(b+"/issues/"+n+"/comments", time.Time{})
		if err != nil {
			return "", err
		}
		cs = append([]Comment{it}, rest...)
	}
	lines := make([]string, 0, limit)
	for _, x := range cs[:min(len(cs), limit)] {
		lines = append(lines, x.User.Login+": "+x.Body)
	}
	return clip(strings.Join(lines, "\n")), nil
}
