package main

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"errors"
	"fmt"
	"io"

	"path/filepath"
	"slices"

	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/github"
)

const (
	// How long the login gh answered is trusted as it was
	loginFor = 24 * time.Hour
	// How long a notification read whole is remembered by its updated_at
	// One that does not change in that time was answered or dropped long ago
	// How often a remembered notification that shows up again renews its memory
)

// Every GitHub call goes through gh so meetproxy keeps no GitHub token
var githubCommands = []command{
	{name: "github whoami", help: "ask gh for the login and keep it", run: func(c cli, _ []string, _ flags) (int, error) { return exitCode(c.githubWhoami()) }},
	{
		name: "github post", args: "<link>", least: 1, most: 1, flags: []string{"session"},
		help: "post the text on stdin in the thread of a link and print its link",
		run:  func(c cli, a []string, _ flags) (int, error) { return c.githubPost(a[0]) },
	},
	{
		name: "github react", args: "<link>", least: 1, most: 1, flags: []string{"react"}, help: "add the reaction --react to what a link points at",
		run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubReact(a[0], f.react)) },
	},
	{
		name: "github update", args: "<reply link>", least: 1, most: 1, help: "replace a reply with the text on stdin",
		run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.githubEdit(a[0], true)) },
	},
	{
		name: "github delete", args: "<reply link>", least: 1, most: 1, help: "delete a reply",
		run: func(c cli, a []string, _ flags) (int, error) { return exitCode(c.githubEdit(a[0], false)) },
	},
	{
		name: "github read", args: "<link>", least: 1, most: 1, flags: []string{"limit"}, help: "print the text of the thread a link points at, cut at 4,000 characters",
		run: func(c cli, a []string, f flags) (int, error) { return exitCode(c.githubRead(a[0], f.limit)) },
	},
}

func githubDir(data string) string { return filepath.Join(data, "github") }

func loginFile(data string) string { return filepath.Join(githubDir(data), "user.json") }

func newGitHub() github.Client { return github.New(github.Exec) }

// The client of a call, whose every gh run also ends by the call's deadline
func (c cli) gitHub() github.Client { return github.New(github.ExecBy(c.deadline)) }

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
	login, err := c.githubLogin(c.gitHub(), true)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"user": login})
}

func parseGitHubLink(link string) (messageurl.Link, error) {
	l, ok := messageurl.ParseLink(link)
	if !ok || l.Source != messageurl.GitHub {
		return messageurl.Link{}, fmt.Errorf("%w: not a GitHub issue or pull request link %q", errUsage, link)
	}
	return l, nil
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

// Posts the text on stdin and prints the link of the comment
func (c cli) githubPost(link string) (int, error) {
	l, err := parseGitHubLink(link)
	if err != nil {
		return exitUsage, err
	}
	text, err := readBody(c.in, "github post")
	if err != nil {
		return exitCode(err)
	}
	posted, err := c.gitHub().Post(l, text)
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
	return c.gitHub().React(l, content)
}

// Replaces with the text on stdin or deletes a comment meetproxy posted

func (c cli) githubEdit(reply string, replace bool) error {
	l, err := parseGitHubLink(reply)
	if err != nil {
		return err
	}
	client := c.gitHub()
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

func (c cli) githubRead(link string, limit int) error {
	l, err := parseGitHubLink(link)
	if err != nil {
		return err
	}
	text, err := c.gitHub().Read(l, limit)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"text": text})
}
