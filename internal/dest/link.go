package dest

import (
	"regexp"
	"strconv"
	"strings"
)

// Where a message link points
// The watcher reads links with its own regexes and checks them against the same corpus as this reader
type Link struct {
	Source string
	// Slack channel id
	Channel string
	// Slack message ts
	Ts string
	// Slack ts of the thread the message sits in, the message's own ts at the top of a channel
	ThreadTs string
	// GitHub owner and repository joined by a slash
	Repo   string
	Number int
	Pull   bool
	// GitHub review comment id the link points at
	Discussion string
	// GitHub issue comment id the link points at
	Comment string
}

var (
	slackMessageRe = regexp.MustCompile(`^https://[\w-]+\.slack\.com/archives/([A-Z0-9]+)/p(\d{10})(\d{6})`)
	slackThreadRe  = regexp.MustCompile(`[?&]thread_ts=(\d+\.\d+)`)
	githubItemRe   = regexp.MustCompile(`^https://github\.com/([\w.-]+/[\w.-]+)/(pull|issues)/(\d+)`)
	discussionRe   = regexp.MustCompile(`#discussion_r(\d+)`)
	issueCommentRe = regexp.MustCompile(`#issuecomment-(\d+)`)
)

// Reads a link to a Slack message or to a GitHub issue, pull request or comment
func ParseLink(raw string) (Link, bool) {
	raw = strings.TrimSpace(raw)
	if m := slackMessageRe.FindStringSubmatch(raw); m != nil {
		l := Link{Source: Slack, Channel: m[1], Ts: m[2] + "." + m[3]}
		l.ThreadTs = l.Ts
		if t := slackThreadRe.FindStringSubmatch(raw); t != nil {
			l.ThreadTs = t[1]
		}
		return l, true
	}
	if m := githubItemRe.FindStringSubmatch(raw); m != nil {
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 {
			return Link{}, false
		}
		l := Link{Source: GitHub, Repo: m[1], Number: n, Pull: m[2] == "pull"}
		if d := discussionRe.FindStringSubmatch(raw); d != nil {
			l.Discussion = d[1]
		}
		if c := issueCommentRe.FindStringSubmatch(raw); c != nil {
			l.Comment = c[1]
		}
		return l, true
	}
	return Link{}, false
}

// Slack names direct conversations with a D
// A group direct conversation looks like a channel so only the reader of direct messages knows it
func (l Link) Direct() bool {
	return l.Source == Slack && strings.HasPrefix(l.Channel, "D")
}

// The key every message of one request shares
// 1. Slack direct message: the conversation so it stays one request until it is done
// 2. Slack: the channel and the ts of the thread
// 3. GitHub: the issue or pull request and the review comment of the link when it points at one
// A reply in a review thread names its own comment so a reader that knows the root names that instead
func (l Link) Thread() string {
	switch l.Source {
	case Slack:
		if l.Direct() {
			return SlackDirectThread(l.Channel)
		}
		return "slack:" + l.Channel + ":" + l.ThreadTs
	case GitHub:
		return GitHubThread(l.Repo, l.Number, l.Discussion)
	}
	return ""
}

func SlackDirectThread(channel string) string { return "slack:" + channel }

// An empty root names the whole issue or pull request
func GitHubThread(repo string, number int, root string) string {
	key := "github:" + repo + "#" + strconv.Itoa(number)
	if root != "" {
		key += ":" + root
	}
	return key
}
