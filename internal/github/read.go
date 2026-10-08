package github

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
)

// Characters of a message kept for triage and the summary
const textChars = 4000

// Kinds of message the notifications hold, named as delegations match them
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

var (
	pullURL = regexp.MustCompile(`repos/([\w.-]+/[\w.-]+)/pulls/(\d+)$`)
	itemURL = regexp.MustCompile(`repos/([\w.-]+/[\w.-]+)/(issues|pulls)/(\d+)$`)
)

// The repository and number of an API url
func item(re *regexp.Regexp, url string) (string, int, bool) {
	m := re.FindStringSubmatch(url)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1])
	return m[1], n, err == nil
}

func Ts(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// A comment as a message
// review is true for a comment of the review threads of a pull request
func message(kind string, self bool, c Comment, repo string, number int, review bool) Message {
	m := Message{
		Source: dest.GitHub, Kind: kind, Self: self, Author: c.User.Login, Channel: repo, From: c.User.Login,
		Ts: Ts(c.CreatedAt), Link: c.HTMLURL, Text: clip(c.Body), Repo: repo, Number: number, Association: c.Association,
	}
	if review {
		m.Discussion = strconv.FormatInt(c.Root(), 10)
	}
	m.Thread = dest.GitHubThread(repo, number, m.Discussion)
	return m
}

func clip(s string) string {
	r := []rune(s)
	return string(r[:min(len(r), textChars)])
}

// Matches a mention of the login as a word
func names(me string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(me) + `\b`)
}

func base(repo string) string { return "repos/" + repo }

// The pull request a review was asked for when it was last asked of the user or a team
func (c Client) ReviewRequest(n Notification, me string, after time.Time) ([]Message, error) {
	var pr Comment
	if err := c.Get(n.Subject.URL, &pr); err != nil || pr.HTMLURL == "" {
		return nil, err
	}
	at, err := c.reviewRequested(n, me)
	if err != nil || !at.After(after) {
		return nil, err
	}
	repo, number, _ := item(itemURL, n.Subject.URL)
	m := message(KindReviewRequest, false, pr, repo, number, false)
	m.Ts, m.Text = Ts(at), clip("Review requested: "+n.Subject.Title+"\n"+pr.Body)
	return []Message{m}, nil
}

type timelineEvent struct {
	Event             string    `json:"event"`
	CreatedAt         time.Time `json:"created_at"`
	RequestedReviewer *User     `json:"requested_reviewer"`
	RequestedTeam     *struct{} `json:"requested_team"`
}

// The notification time moves with any activity on the pull request and would reopen a review already done
// A timeline without such an event falls back to the notification time
func (c Client) reviewRequested(n Notification, me string) (time.Time, error) {
	repo, number, ok := item(pullURL, n.Subject.URL)
	if !ok {
		return n.UpdatedAt, nil
	}
	events, err := list[timelineEvent](c, base(repo)+"/issues/"+strconv.Itoa(number)+"/timeline", time.Time{})
	if err != nil {
		return time.Time{}, err
	}
	for _, e := range slices.Backward(events) {
		if e.Event == "review_requested" && ((e.RequestedReviewer != nil && e.RequestedReviewer.Login == me) || e.RequestedTeam != nil) {
			return e.CreatedAt, nil
		}
	}
	return n.UpdatedAt, nil
}

// The comments after after that name the user
// 1. A notification only points at the latest comment, which may not be the one that mentions the user
// 2. The issue or pull request itself counts when it was opened after after and names the user
func (c Client) Mentions(n Notification, me string, after time.Time) ([]Message, error) {
	repo, number, ok := item(itemURL, n.Subject.URL)
	if !ok {
		return nil, nil
	}
	named := names(me)
	fresh := func(x Comment) bool { return named.MatchString(x.Body) && x.CreatedAt.After(after) }
	out := []Message{}
	threads, err := c.threads(repo, number, n.Subject.Type == "PullRequest", after)
	if err != nil {
		return nil, err
	}
	for i, cs := range threads {
		for _, x := range cs {
			if fresh(x) {
				out = append(out, message(KindMention, x.User.Login == me, x, repo, number, i == 1))
			}
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	var it Comment
	if err := c.Get(n.Subject.URL, &it); err != nil {
		return nil, err
	}
	if !fresh(it) {
		return nil, nil
	}
	return []Message{message(KindMention, it.User.Login == me, it, repo, number, false)}, nil
}

// The comments on the issue and, of a pull request, those in its review threads
func (c Client) threads(repo string, number int, pull bool, since time.Time) ([2][]Comment, error) {
	var out [2][]Comment
	var err error
	b := base(repo)
	if out[0], err = c.Comments(b+"/issues/"+strconv.Itoa(number)+"/comments", since); err != nil || !pull {
		return out, err
	}
	out[1], err = c.Comments(b+"/pulls/"+strconv.Itoa(number)+"/comments", since)
	return out, err
}

// Comments others wrote after after on a pull request the user opened
// A comment naming the user comes as a mention instead so it is left out here
func (c Client) OwnPull(n Notification, me string, after time.Time) ([]Message, error) {
	repo, number, ok := item(pullURL, n.Subject.URL)
	if !ok {
		return nil, nil
	}
	var pr Comment
	if err := c.Get(n.Subject.URL, &pr); err != nil || pr.User.Login != me {
		return nil, err
	}
	threads, err := c.threads(repo, number, true, after)
	if err != nil {
		return nil, err
	}
	named := names(me)
	out := []Message{}
	for i, cs := range threads {
		for _, x := range cs {
			if x.User.Login != "" && x.User.Login != me && !x.User.Bot() && !named.MatchString(x.Body) && x.CreatedAt.After(after) {
				out = append(out, message(KindOwnPull, false, x, repo, number, i == 1))
			}
		}
	}
	return out, nil
}

// The review thread a link sits in, the root comment of it
// The link of a reply names the reply so its root is looked up among cs first and asked of GitHub otherwise
func (c Client) root(l dest.Link, cs []Comment) (int64, error) {
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
func (c Client) inThread(l dest.Link, cs []Comment) ([]Comment, int64, error) {
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

func marked(body string) bool { return strings.HasSuffix(strings.TrimSpace(body), Mark) }

// Whether the thread of the link has an answer after ts
// 1. A comment of the user or one that ends with the meetproxy mark such as one a teammate's meetproxy posted
// 2. A review thread link only counts replies in that thread
// 3. A review the user submitted on the pull request after ts counts too
func (c Client) Covered(l dest.Link, me string, ts time.Time) (bool, error) {
	answer := func(x Comment) bool { return (x.User.Login == me || marked(x.Body)) && x.CreatedAt.After(ts) }
	n := strconv.Itoa(l.Number)
	if l.Discussion != "" {
		cs, err := c.Comments(base(l.Repo)+"/pulls/"+n+"/comments", ts)
		if err != nil {
			return false, err
		}
		replies, _, err := c.inThread(l, cs)
		return slices.ContainsFunc(replies, answer), err
	}
	cs, err := c.Comments(base(l.Repo)+"/issues/"+n+"/comments", ts)
	switch {
	case err != nil:
		return false, err
	case slices.ContainsFunc(cs, answer):
		return true, nil
	case !l.Pull:
		return false, nil
	}
	reviews, err := c.Comments(base(l.Repo)+"/pulls/"+n+"/reviews", time.Time{})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(reviews, func(r Comment) bool { return r.User.Login == me && r.SubmittedAt.After(ts) }), nil
}

// Comments others wrote in the thread of the link after after
// A review thread counts only replies in that thread
func (c Client) Replies(l dest.Link, me string, after time.Time) ([]Message, error) {
	n := strconv.Itoa(l.Number)
	var cs []Comment
	var err error
	review := l.Discussion != ""
	if review {
		cs, err = c.Comments(base(l.Repo)+"/pulls/"+n+"/comments", after)
		if err == nil {
			cs, _, err = c.inThread(l, cs)
		}
	} else {
		cs, err = c.Comments(base(l.Repo)+"/issues/"+n+"/comments", after)
	}
	if err != nil {
		return nil, err
	}
	out := []Message{}
	for _, x := range cs {
		if x.User.Login != "" && x.User.Login != me && !x.User.Bot() && !marked(x.Body) && x.CreatedAt.After(after) {
			out = append(out, message("", false, x, l.Repo, l.Number, review))
		}
	}
	return out, nil
}

// The text of what the link points at, one line per comment
// 1. A review thread: its root comment and the replies
// 2. A comment: that comment
// 3. An issue or pull request: its title and body and the first comments
// Read for triage and routing only so it is never stored
func (c Client) Read(l dest.Link, limit int) (string, error) {
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
