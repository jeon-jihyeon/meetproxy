package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/guard"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
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

func (c cli) locate(terms []string, limit int) error {
	cs, err := locmap.New(c.data).Locate(terms, limit)
	if err != nil {
		return err
	}
	for _, cand := range cs {
		fmt.Fprintf(c.out, "%g\t%s\t%s\t%s\n", cand.Score, cand.Name, cand.Abs(), strings.Join(cand.Topics, " | "))
	}
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

func (c cli) close(topic string, keywords, paths []string) error {
	if topic == "" {
		return fmt.Errorf("%w: close needs --topic", errUsage)
	}
	store := relay.New(c.data)
	r, err := store.Current(c.session)
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	ps, err := store.Evidence(r.Id, paths, dir)
	if err != nil {
		return err
	}
	// Each step repeats without harm so a close that failed part way runs again
	// 1. a done request may be marked done again
	// 2. the location map keeps the last record of a relay
	if err := inbox.New(c.data).DoneByLink(r.Origin, c.session, c.now); err != nil {
		return err
	}
	e := locmap.Entry{RelayId: r.Id, Topic: topic, Keywords: keywords, Paths: ps, RecordedAt: c.now.UTC()}
	if err := locmap.New(c.data).Add(e); err != nil {
		return err
	}
	if _, err := store.Close(c.session, c.now); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s closed · %d paths\n", r.Id, len(ps))
	return nil
}

// What the plugin mod reads once per tick
type tick struct {
	Protocol int `json:"protocol"`
	// Root and name of the place of the session directory
	Place string `json:"place"`
	Name  string `json:"name"`
	// Every waiting request oldest first
	Waiting []waitingRow `json:"waiting"`
	// Whether Slack is read with a token and the user's answer to setting one up
	Slack slackState `json:"slack"`
	// Threads read for follow-ups by the session holding the lease of their source
	Watch []posts.Watch `json:"watch"`
	// Reactions the lease holder adds to request links
	Acks []ackRow `json:"acks"`
}

type ackRow struct {
	Id    string `json:"id"`
	Link  string `json:"link"`
	React string `json:"react"`
}

type waitingRow struct {
	Id     string       `json:"id"`
	Status inbox.Status `json:"status"`
	Place  string       `json:"place"`
	Name   string       `json:"name"`
	// Unix seconds
	Added int64  `json:"added"`
	Link  string `json:"link"`
	// The request belongs to the place of the session
	Here       bool   `json:"here"`
	Delegation string `json:"delegation,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	// A held request whose time came
	Due bool `json:"due,omitempty"`
	// Unix seconds a held request is asked about again, zero when it waits for the user
	Until int64 `json:"until,omitempty"`
}

// Requests older than this get no reaction since one so late reads as noise
const ackWithin = 24 * time.Hour

// With busy the takes of the session get a heartbeat so a long turn never loses them
// The heartbeat runs under the scope so a take it keeps always has a marker the launcher sees
func (c cli) tick(cwd string, busy bool) error {
	if cwd == "" {
		return fmt.Errorf("%w: tick needs --cwd", errUsage)
	}
	root, name := locmap.Place(cwd)
	store := inbox.New(c.data)
	if busy && c.session != "" {
		touch := func() error { return store.Touch(c.session, c.now) }
		if err := withScope(c.data, c.session, c.now, touch); err != nil {
			return err
		}
	}
	items, err := store.Waiting(c.now)
	if err != nil {
		return err
	}
	rows := make([]waitingRow, 0, len(items))
	for _, it := range items {
		row := waitingRow{
			Id: it.Id, Status: it.Status, Place: it.Place, Name: it.Name, Added: it.AddedAt.Unix(), Link: it.Link,
			Here: it.At(root, name), Delegation: it.Delegation, Priority: it.Priority, Due: it.Due(c.now),
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
	acks, err := c.acks(store)
	if err != nil {
		return err
	}
	if watch == nil {
		watch = []posts.Watch{}
	}
	return json.NewEncoder(c.out).Encode(tick{
		Protocol: protocol, Place: root, Name: name, Waiting: rows, Slack: readSlackState(c.data, false), Watch: watch, Acks: acks,
	})
}

// Reactions still owed on request links
// 1. eyes on a request queued within ackWithin
// 2. done on a request answered with a post
// 3. handoff on a request the automatic attempt gave back when its delegation asks for a note
func (c cli) acks(store inbox.Store) ([]ackRow, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	ledger := posts.New(c.data)
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
			posted, err := ledger.Posted(it.Id)
			if err != nil {
				return nil, err
			}
			if posted {
				owed(inbox.AckDone)
			}
		case it.Status == inbox.StatusAsk && it.Mode == inbox.ModeAuto && it.Handoff:
			owed(inbox.AckSeen)
			owed(inbox.AckHandoff)
		case it.Status != inbox.StatusDone:
			owed(inbox.AckSeen)
		}
	}
	return out, nil
}
