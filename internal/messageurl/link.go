package messageurl

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	GitHub = "github"
	Slack  = "slack"
)

type Link struct {
	Source     string
	Channel    string
	Ts         string
	ThreadTs   string
	Repo       string
	Number     int
	Pull       bool
	Discussion string
	Comment    string
}

var (
	slackMessage = regexp.MustCompile(`^https://[\w-]+\.slack\.com/archives/([A-Z0-9]+)/p(\d{10})(\d{6})`)
	slackThread  = regexp.MustCompile(`[?&]thread_ts=(\d+\.\d+)`)
	githubItem   = regexp.MustCompile(`^https://github\.com/([\w.-]+/[\w.-]+)/(pull|issues)/(\d+)`)
	discussion   = regexp.MustCompile(`#discussion_r(\d+)`)
	issueComment = regexp.MustCompile(`#issuecomment-(\d+)`)
)

func ParseLink(raw string) (Link, bool) {
	raw = strings.TrimSpace(raw)
	if match := slackMessage.FindStringSubmatch(raw); match != nil {
		link := Link{Source: Slack, Channel: match[1], Ts: match[2] + "." + match[3]}
		link.ThreadTs = link.Ts
		if thread := slackThread.FindStringSubmatch(raw); thread != nil {
			link.ThreadTs = thread[1]
		}
		return link, true
	}
	if match := githubItem.FindStringSubmatch(raw); match != nil {
		number, err := strconv.Atoi(match[3])
		if err != nil || number <= 0 {
			return Link{}, false
		}
		link := Link{Source: GitHub, Repo: match[1], Number: number, Pull: match[2] == "pull"}
		if item := discussion.FindStringSubmatch(raw); item != nil {
			link.Discussion = item[1]
		}
		if item := issueComment.FindStringSubmatch(raw); item != nil {
			link.Comment = item[1]
		}
		return link, true
	}
	return Link{}, false
}
