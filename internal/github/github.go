// Package github reads and writes GitHub through the gh command the user is logged in to
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// The line every post of meetproxy ends with
const Mark = "_Written by Claude on behalf of the user_"

// One call of gh is bounded so a hung network never holds a tick up
const callTimeout = time.Minute

// Runs gh with the arguments and stdin and returns stdout
type Runner func(args []string, stdin []byte) ([]byte, error)

// Runs the gh on PATH
// meetproxy keeps no token of its own so gh is the only way it reaches GitHub
func Exec(args []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args[:min(len(args), 2)], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type Client struct{ run Runner }

func New(run Runner) Client { return Client{run: run} }

type User struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// Bots answer automatically so they never ask the user anything
func (u User) Bot() bool {
	return u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]")
}

type Notification struct {
	Id        string    `json:"id"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// What a notification is remembered by
func (n Notification) Key() string {
	if n.Id != "" {
		return n.Id
	}
	return n.Subject.URL
}

// An issue, a pull request or a comment on either
type Comment struct {
	Id int64 `json:"id"`
	// Root comment of the review thread a reply sits in, zero for the root and any other comment
	InReplyTo   int64     `json:"in_reply_to_id"`
	User        User      `json:"user"`
	Body        string    `json:"body"`
	Title       string    `json:"title"`
	HTMLURL     string    `json:"html_url"`
	CreatedAt   time.Time `json:"created_at"`
	SubmittedAt time.Time `json:"submitted_at"`
	Association string    `json:"author_association"`
}

// The comment that starts the review thread the comment sits in
func (c Comment) Root() int64 {
	if c.InReplyTo != 0 {
		return c.InReplyTo
	}
	return c.Id
}

func (c Client) Login() (string, error) {
	out, err := c.run([]string{"api", "user", "-q", ".login"}, nil)
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", errors.New("could not read the GitHub login")
	}
	return login, nil
}

// Notifications that involve the user updated since since, without marking them read
func (c Client) Notifications(since time.Time) ([]Notification, error) {
	return list[Notification](c, "notifications", since, "-f", "participating=true")
}

// Every item of a list across its pages
// A zero since reads every item
func list[T any](c Client, path string, since time.Time, extra ...string) ([]T, error) {
	args := append([]string{"api", "--paginate", "--slurp", path, "-X", "GET"}, extra...)
	if !since.IsZero() {
		args = append(args, "-f", "since="+since.UTC().Format(time.RFC3339))
	}
	out, err := c.run(args, nil)
	if err != nil {
		return nil, err
	}
	var pages [][]T
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, fmt.Errorf("gh api %s: %w", path, err)
	}
	var items []T
	for _, p := range pages {
		items = append(items, p...)
	}
	return items, nil
}

func (c Client) Comments(path string, since time.Time) ([]Comment, error) {
	return list[Comment](c, path, since)
}

func (c Client) Get(path string, v any) error {
	out, err := c.run([]string{"api", path}, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gh api %s: %w", path, err)
	}
	return nil
}

// Sends the body as JSON on stdin so no text of it ever shows in a process list
func (c Client) send(method, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.run([]string{"api", "-X", method, path, "--input", "-"}, b)
}
