package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/github"
	"github.com/jeon-jihyeon/meetproxy/internal/posts"
)

const (
	// How long the login gh answered is trusted as it was
	loginFor = 24 * time.Hour
	// How long a notification read whole is remembered by its updated_at
	// One that does not change in that time was answered or dropped long ago
	seenFor = 24 * time.Hour
	// How often a remembered notification that shows up again renews its memory
	seenRenew = time.Hour
)

// Every GitHub call goes through gh so meetproxy keeps no GitHub token
var githubCommands = []command{
	{name: "github whoami", help: "ask gh for the login and keep it", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.githubWhoami()) }},
	{
		name: "github mentions", flags: []string{"after"}, help: "print comments that mention the user after --after oldest first",
		run: func(c cli, _ []string, f flags) (int, error) {
			return exitCode(c.githubNotifications(mentionReader, f.after))
		},
	},
	{
		name: "github own", flags: []string{"after"}, help: "print comments of others on the user's pull requests after --after",
		run: func(c cli, _ []string, f flags) (int, error) {
			return exitCode(c.githubNotifications(ownReader, f.after))
		},
	},
	{
		name: "github review-requests", flags: []string{"after"}, help: "print reviews asked of the user after --after",
		run: func(c cli, _ []string, f flags) (int, error) {
			return exitCode(c.githubNotifications(reviewReader, f.after))
		},
	},
	{
		name: "github seen", help: "remember the notifications the reads went through and print the newest notification time",
		run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.githubSeen()) },
	},
	{
		name: "github covered", args: "<link>", least: 1, most: 1, flags: []string{"ts"},
		help: "print whether the user or meetproxy answered in the thread after --ts",
		run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubCovered(a[0], f.ts)) },
	},
	{
		name: "github replies", args: "<link>", least: 1, most: 1, flags: []string{"after"},
		help: "print the comments in the thread of a link after --after that others wrote",
		run:  func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubReplies(a[0], f.after)) },
	},
	{
		name: "github post", args: "<link>", least: 1, most: 1, flags: []string{"session"},
		help: "post the text on stdin in the thread of a link after the can-post check and print its link",
		run:  func(c cli, a []string, _ flags) (int, error) { return c.githubPost(a[0]) },
	},
	{
		name: "github react", args: "<link>", least: 1, most: 1, flags: []string{"react"}, help: "add the reaction --react to what a link points at",
		run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubReact(a[0], f.react)) },
	},
	{
		name: "github update", args: "<reply link>", least: 1, most: 1, help: "replace a reply of the ledger with the text on stdin",
		run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.githubEdit(a[0], true)) },
	},
	{
		name: "github delete", args: "<reply link>", least: 1, most: 1, help: "delete a reply of the ledger",
		run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.githubEdit(a[0], false)) },
	},
	{
		name: "github read", args: "<link>", least: 1, most: 1, flags: []string{"limit"}, help: "print the text of the thread a link points at",
		run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubRead(a[0], f.limit)) },
	},
}

func githubDir(data string) string { return filepath.Join(data, "github") }

func loginFile(data string) string { return filepath.Join(githubDir(data), "user.json") }

func seenFile(data string) string { return filepath.Join(githubDir(data), "seen.json") }

// Notifications a read went through, kept until github seen records them once their messages are settled
// One file per reader so the readers never drop each other's marks
func pendingFile(data, reader string) string {
	return filepath.Join(githubDir(data), "pending-"+reader+".json")
}

func newGitHub() github.Client { return github.New(github.Exec) }

type githubLogin struct {
	Login string    `json:"login"`
	At    time.Time `json:"at"`
}

// The login gh is logged in as, asked of gh when live or when the kept one is too old
func (c cli) githubLogin(client github.Client, live bool) (string, error) {
	var kept githubLogin
	if !live {
		if found, err := fileio.ReadJSON(loginFile(c.data), &kept); found && err == nil && kept.Login != "" && c.now.Sub(kept.At) < loginFor {
			return kept.Login, nil
		}
	}
	login, err := client.Login()
	if err != nil {
		return "", err
	}
	return login, fileio.WriteJSON(loginFile(c.data), githubLogin{login, c.now.UTC()})
}

func (c cli) githubWhoami() error {
	login, err := c.githubLogin(newGitHub(), true)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"user": login})
}

// Unix seconds as the mod passes them
func unixArg(name, v string) (time.Time, error) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || v == "" {
		return time.Time{}, fmt.Errorf("%w: %s must be unix seconds, not %q", errUsage, name, v)
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)), nil
}

// A reader of one kind of notification
type notificationReader struct {
	name  string
	takes func(n github.Notification) bool
	read  func(c github.Client, n github.Notification, me string, after time.Time) ([]github.Message, error)
}

var (
	mentionReader = notificationReader{
		"mentions", func(n github.Notification) bool { return n.Reason == "mention" || n.Reason == "team_mention" },
		github.Client.Mentions,
	}
	ownReader = notificationReader{
		"own", func(n github.Notification) bool {
			return (n.Reason == "author" || n.Reason == "comment") && n.Subject.Type == "PullRequest"
		},
		github.Client.OwnPull,
	}
	reviewReader = notificationReader{
		"review-requests", func(n github.Notification) bool { return n.Reason == "review_requested" },
		github.Client.ReviewRequest,
	}
)

type seenMark struct {
	Updated time.Time `json:"updated"`
	At      time.Time `json:"at"`
}

type pending struct {
	// Unix seconds of the newest notification the read went through
	Newest string `json:"newest"`
	// updated_at of each notification read whole
	Marks map[string]time.Time `json:"marks"`
}

// Messages of the notifications of one kind after --after oldest first
// 1. A notification whose updated_at did not change since it was read whole is skipped without reading its subject
// 2. What was read whole is only remembered by github seen, which the mod runs once the messages are settled
// so messages a failed check never settled are read again
func (c cli) githubNotifications(r notificationReader, afterArg string) error {
	after, err := unixArg("--after", afterArg)
	if err != nil {
		return err
	}
	client := newGitHub()
	me, err := c.githubLogin(client, false)
	if err != nil {
		return err
	}
	notes, err := client.Notifications(after)
	if err != nil {
		return err
	}
	seen := c.readSeen()
	renewed := false
	p := pending{Marks: map[string]time.Time{}}
	out := []github.Message{}
	for _, n := range notes {
		if ts := github.Ts(n.UpdatedAt); p.Newest == "" || tsLess(p.Newest, ts) {
			p.Newest = ts
		}
		if !r.takes(n) {
			continue
		}
		if s, ok := seen[n.Key()]; ok && s.Updated.Equal(n.UpdatedAt) {
			if c.now.Sub(s.At) > seenRenew {
				seen[n.Key()], renewed = seenMark{s.Updated, c.now.UTC()}, true
			}
			continue
		}
		msgs, err := r.read(client, n, me, after)
		if err != nil {
			return err
		}
		out = append(out, msgs...)
		p.Marks[n.Key()] = n.UpdatedAt
	}
	if err := fileio.WriteJSON(pendingFile(c.data, r.name), p); err != nil {
		return err
	}
	if renewed {
		if err := fileio.WriteJSON(seenFile(c.data), seen); err != nil {
			return err
		}
	}
	return c.writeGitHub(out)
}

// A lost file only makes notifications read whole again
func (c cli) readSeen() map[string]seenMark {
	seen := map[string]seenMark{}
	if _, err := fileio.ReadJSON(seenFile(c.data), &seen); err != nil || seen == nil {
		return map[string]seenMark{}
	}
	return seen
}

// Remembers the notifications the last reads went through and prints the newest notification time for the cursor
// Marks no read renewed for seenFor are forgotten so the file does not grow
func (c cli) githubSeen() error {
	seen := c.readSeen()
	newest := ""
	for _, r := range []notificationReader{mentionReader, ownReader, reviewReader} {
		var p pending
		file := pendingFile(c.data, r.name)
		found, err := fileio.ReadJSON(file, &p)
		if !found {
			continue
		}
		if err == nil {
			for key, updated := range p.Marks {
				seen[key] = seenMark{updated, c.now.UTC()}
			}
			if newest == "" || tsLess(newest, p.Newest) {
				newest = p.Newest
			}
		}
		if err := os.Remove(file); err != nil {
			return err
		}
	}
	for key, s := range seen {
		if c.now.Sub(s.At) > seenFor {
			delete(seen, key)
		}
	}
	if err := fileio.WriteJSON(seenFile(c.data), seen); err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"newest": newest})
}

func (c cli) writeGitHub(out []github.Message) error {
	slices.SortStableFunc(out, func(a, b github.Message) int {
		if tsLess(a.Ts, b.Ts) {
			return -1
		}
		if tsLess(b.Ts, a.Ts) {
			return 1
		}
		return 0
	})
	return writeJSON(c.out, out)
}

func parseGitHubLink(link string) (dest.Link, error) {
	l, ok := dest.ParseLink(link)
	if !ok || l.Source != dest.GitHub {
		return dest.Link{}, fmt.Errorf("%w: not a GitHub issue or pull request link %q", errUsage, link)
	}
	return l, nil
}

func (c cli) githubCovered(link, ts string) error {
	l, err := parseGitHubLink(link)
	if err != nil {
		return err
	}
	at, err := unixArg("--ts", ts)
	if err != nil {
		return err
	}
	client := newGitHub()
	me, err := c.githubLogin(client, false)
	if err != nil {
		return err
	}
	covered, err := client.Covered(l, me, at)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]bool{"covered": covered})
}

func (c cli) githubReplies(link, afterArg string) error {
	l, err := parseGitHubLink(link)
	if err != nil {
		return err
	}
	after, err := unixArg("--after", afterArg)
	if err != nil {
		return err
	}
	client := newGitHub()
	me, err := c.githubLogin(client, false)
	if err != nil {
		return err
	}
	out, err := client.Replies(l, me, after)
	if err != nil {
		return err
	}
	return c.writeGitHub(out)
}

func readBody(in io.Reader, what string) (string, error) {
	b, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", fmt.Errorf("%w: %s needs the text on stdin", errUsage, what)
	}
	return text, nil
}

// Posts the text on stdin after the same check as can-post and prints the link of the comment
func (c cli) githubPost(link string) (int, error) {
	l, err := parseGitHubLink(link)
	if err != nil {
		return exitUsage, err
	}
	text, err := readBody(c.in, "github post")
	if err != nil {
		return exitCode(err)
	}
	allowed, err := c.mayPost(link)
	if err != nil {
		return exitFailed, err
	}
	if !allowed {
		return c.verdict(false)
	}
	posted, err := newGitHub().Post(l, text)
	if err != nil {
		return exitFailed, err
	}
	fmt.Fprintln(c.out, posted)
	return 0, nil
}

func (c cli) githubReact(link, content string) error {
	l, err := parseGitHubLink(link)
	if err != nil {
		return err
	}
	if !slices.Contains(github.Reactions, content) {
		return fmt.Errorf("%w: --react is one of %v, not %q", errUsage, github.Reactions, content)
	}
	return newGitHub().React(l, content)
}

// Replaces with the text on stdin or deletes a comment meetproxy posted
// Only a reply the ledger holds may change so a request can never make meetproxy edit another comment
func (c cli) githubEdit(reply string, replace bool) error {
	l, err := parseGitHubLink(reply)
	if err != nil {
		return err
	}
	if _, err := posts.New(c.data).Find(reply); err != nil {
		return err
	}
	client := newGitHub()
	if !replace {
		return usageOf(client.Delete(l))
	}
	text, err := readBody(c.in, "github update")
	if err != nil {
		return err
	}
	return usageOf(client.Edit(l, text))
}

// A link to no comment is fixed by a corrected command line
func usageOf(err error) error {
	if errors.Is(err, github.ErrNotComment) {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	return err
}

// The text a link points at, read for triage and routing only so it is never stored
func (c cli) githubRead(link string, limit int) error {
	l, err := parseGitHubLink(link)
	if err != nil {
		return err
	}
	text, err := newGitHub().Read(l, limit)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"text": text})
}
