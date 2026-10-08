package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/guard"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/posts"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

type cli struct {
	data    string
	session string
	now     time.Time
	in      io.Reader
	out     io.Writer
}

func printVersion(c cli, _ []string, _ flags) (int, error) {
	_, err := fmt.Fprintln(c.out, version)
	return exitCode(err)
}

func printProtocol(c cli, _ []string, _ flags) (int, error) {
	_, err := fmt.Fprintln(c.out, protocol)
	return exitCode(err)
}

func allow(c cli, args []string, _ flags) (int, error) {
	return exitCode(dest.New(c.data).Add(args[0]))
}

func (c cli) open(origin, target string) error {
	var r relay.Relay
	err := withScope(c.data, c.session, c.now, func() (err error) {
		r, err = relay.New(c.data).Open(c.session, origin, target, c.now)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, r.Id)
	return nil
}

func parseLink(raw string) (dest.Location, error) {
	loc, ok := dest.Parse(raw)
	if !ok {
		return dest.Location{}, fmt.Errorf("link must be a Slack or GitHub link %q", raw)
	}
	return loc, nil
}

func (c cli) dest(raw string) (int, error) {
	loc, err := parseLink(raw)
	if err != nil {
		return exitFailed, err
	}
	allowed, err := dest.New(c.data).Allowed(loc)
	if err != nil {
		return exitFailed, err
	}
	return c.verdict(allowed)
}

// Prints allowed or denied and exits 1 when denied
func (c cli) verdict(allowed bool) (int, error) {
	if !allowed {
		fmt.Fprintln(c.out, "denied")
		return exitFailed, nil
	}
	fmt.Fprintln(c.out, "allowed")
	return 0, nil
}

func (c cli) allowed() error {
	ps, err := dest.New(c.data).Patterns()
	for _, p := range ps {
		fmt.Fprintln(c.out, p)
	}
	return err
}

// The check the meetproxy post tool makes before it posts
func (c cli) canPost(link string) (int, error) {
	allowed, err := c.mayPost(link)
	if err != nil {
		return exitFailed, err
	}
	return c.verdict(allowed)
}

func (c cli) mayPost(link string) (bool, error) {
	loc, err := parseLink(link)
	if err != nil {
		return false, err
	}
	sc, _, err := scopeOf(c.data, c.session, c.now)
	if err != nil {
		return false, err
	}
	reason, err := denial(c.data, []guard.Post{{At: loc}}, sc)
	return reason == "", err
}

// Why posts are denied in the scope sc
// An empty scope means the session handles nothing so only the allow list counts
func denial(data string, posts []guard.Post, sc scope) (string, error) {
	o, _ := dest.Parse(sc.origin)
	t, _ := dest.Parse(sc.target)
	return guard.Decide(posts, o, t, sc.mayApprove, dest.New(data).Allowed)
}

func (c cli) close() error {
	store := relay.New(c.data)
	r, err := store.Current(c.session)
	if err != nil {
		return err
	}
	// A done request may be marked done again so a close that failed part way runs again
	if err := c.settleOrigin(r.Origin); err != nil {
		return err
	}
	if _, err := store.Close(c.session, c.now); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s closed\n", r.Id)
	return nil
}

// Marks done the request a relay was opened for
// A relay opened by hand for a link no request came from settles nothing
func (c cli) settleOrigin(origin string) error {
	store := inbox.New(c.data)
	it, err := store.ByOrigin(origin)
	if errors.Is(err, inbox.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return store.DoneById(it.Id, c.session, c.now)
}

// What the plugin mod reads once per tick
type tick struct {
	Protocol int `json:"protocol"`
	// Every request no session works on oldest first
	Waiting []waitingRow `json:"waiting"`
	// Whether Slack is read with a token and the user's answer to setting one up
	Slack slackState `json:"slack"`
	// Threads read for follow-ups by the session holding the lease of their source
	Watch []posts.Watch `json:"watch"`
	// Reactions the lease holder adds to request links
	Acks []ackRow `json:"acks"`
	// Leases that still last by source so a session holds one only when it runs out
	Leases map[string]inbox.Lease `json:"leases"`
}

type ackRow struct {
	Id    string `json:"id"`
	Link  string `json:"link"`
	React string `json:"react"`
}

type waitingRow struct {
	Id     string       `json:"id"`
	Status inbox.Status `json:"status"`
	// The list offers it now
	// A held request before its time or a question the requester may still answer waits
	Open   bool   `json:"open"`
	Link   string `json:"link"`
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
	// Untrusted text
	Summary string `json:"summary,omitempty"`
	Task    string `json:"task,omitempty"`
	// Delegation that caught the request so the inbox can be listed by it
	Delegation string `json:"delegation,omitempty"`
	// Unix seconds
	Added int64 `json:"added"`
	// When the newest message of the thread was written
	Last time.Time `json:"last"`
	// Unix seconds a held request opens again, zero when it waits for the user
	Until int64 `json:"until,omitempty"`
}

// Requests older than this get no reaction since one so late reads as noise
const ackWithin = 24 * time.Hour

// The inbox is listed once and the ledger read at most once however many requests wait
// With a session the tick is also its heartbeat
func (c cli) tick() error {
	store := inbox.New(c.data)
	if c.session != "" {
		if err := store.Beat(c.session, c.now); err != nil {
			return err
		}
	}
	all, err := store.List()
	if err != nil {
		return err
	}
	rows := []waitingRow{}
	for _, it := range inbox.Waiting(all, c.now) {
		row := waitingRow{
			Id: it.Id, Status: it.Status, Open: it.Open(c.now), Link: it.Link, Source: it.Source, Author: it.Author,
			Summary: it.Summary, Task: it.Task, Delegation: it.Delegation, Added: it.AddedAt.Unix(), Last: it.Last(),
		}
		if !it.HeldUntil.IsZero() {
			row.Until = it.HeldUntil.Unix()
		}
		rows = append(rows, row)
	}
	watch, err := posts.New(c.data).Watches(c.now)
	if err != nil {
		return err
	}
	acks, err := c.acks(all)
	if err != nil {
		return err
	}
	if watch == nil {
		watch = []posts.Watch{}
	}
	leases, err := store.Leases(c.now)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(tick{
		Protocol: protocol, Waiting: rows, Slack: readSlackState(c.data, false), Watch: watch, Acks: acks, Leases: leases,
	})
}

// Reactions still owed on request links
// 1. eyes on a request queued within ackWithin
// 2. done on a request answered with a post
func (c cli) acks(all []inbox.Item) ([]ackRow, error) {
	var answered map[string]bool
	out := []ackRow{}
	for _, it := range all {
		if c.now.Sub(it.UpdatedAt) > ackWithin {
			continue
		}
		owed := func(react string) {
			if !slices.Contains(it.Acked, react) {
				out = append(out, ackRow{it.Id, it.Link, react})
			}
		}
		switch {
		case it.Status == inbox.StatusDone && it.Reason != "expired":
			if answered == nil {
				var err error
				if answered, err = posts.New(c.data).Answered(); err != nil {
					return nil, err
				}
			}
			if answered[it.Id] {
				owed(inbox.AckDone)
			}
		case it.Status != inbox.StatusDone:
			owed(inbox.AckSeen)
		}
	}
	return out, nil
}
