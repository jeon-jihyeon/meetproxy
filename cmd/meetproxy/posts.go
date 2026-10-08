package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/posts"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

// What the post tool hands over once a reply went out
type postedReply struct {
	Reply string `json:"reply"`
	Body  string `json:"body"`
	Kind  string `json:"kind"`
}

// Records a reply the session posted in the scope it handles
// The thread of the origin is then watched for follow-ups
func (c cli) postsAdd() error {
	var in postedReply
	if err := json.NewDecoder(c.in).Decode(&in); err != nil {
		return fmt.Errorf("%w: posts add reads {reply, body, kind} as JSON: %w", errUsage, err)
	}
	if in.Kind == "" {
		in.Kind = posts.KindAnswer
	}
	if in.Kind != posts.KindAnswer && in.Kind != posts.KindQuestion {
		return fmt.Errorf("%w: kind is answer or question, not %q", errUsage, in.Kind)
	}
	sc, open, err := scopeOf(c.data, c.session, c.now)
	if err != nil {
		return err
	}
	if !open {
		return errors.New("the session handles no request so there is nothing to record the post under")
	}
	p := posts.Post{
		Request: inbox.IdOf(sc.origin), Relay: c.relayId(), Origin: sc.origin, Reply: in.Reply, Body: in.Body,
		Mode: posts.ModeManual, Kind: in.Kind, Session: c.session,
	}
	it, err := inbox.New(c.data).ByOrigin(sc.origin)
	switch {
	case err == nil:
		p.Request, p.Delegation, p.Mode = it.Id, it.Delegation, posts.ModeInbox
	case !errors.Is(err, inbox.ErrNotFound):
		return err
	}
	if err := posts.New(c.data).Add(p, replyTs(in.Reply, c.now), c.now); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "recorded", p.Request)
	return nil
}

// The id of the relay the session has open or settled in this turn
func (c cli) relayId() string {
	relays := relay.New(c.data)
	if r, err := relays.Current(c.session); err == nil {
		return r.Id
	}
	if r, ended, err := relays.Ended(c.session, c.now); err == nil && ended {
		return r.Id
	}
	return ""
}

// The ts of a Slack reply from its link and now for anything else
func replyTs(reply string, now time.Time) string {
	if at, err := parseSlackLink(reply); err == nil {
		return at.ts
	}
	return strconv.FormatInt(now.Unix(), 10)
}

func (c cli) postsList(limit int) error {
	ps, err := posts.New(c.data).List(limit)
	if err != nil {
		return err
	}
	if ps == nil {
		ps = []posts.Post{}
	}
	return writeJSON(c.out, ps)
}

// Prints the post of a reply and exits 1 when the ledger has none
func (c cli) postsFind(reply string) error {
	p, err := posts.New(c.data).Find(reply)
	if err != nil {
		return err
	}
	return writeJSON(c.out, p)
}

func (c cli) postsRetract(reply string) error {
	p, err := posts.New(c.data).Retract(reply, c.now)
	if err == nil {
		fmt.Fprintln(c.out, "retracted", p.Reply)
	}
	return err
}

func (c cli) watchSeen(request, ts string) error { return posts.New(c.data).Seen(request, ts) }

// Records a message read and not queued and moves the cursor past it
func (c cli) inboxIgnore(link string, f flags) error {
	rec := inbox.Ignored{Link: link, From: f.from, Delegation: f.delegation, Reason: f.reason}
	store := inbox.New(c.data)
	if err := store.Ignore(rec, c.now); err != nil {
		return err
	}
	if f.ts == "" {
		return nil
	}
	return store.Advance(f.key, f.ts)
}

func (c cli) inboxAcked(id, react string) error {
	_, err := inbox.New(c.data).Ack(id, react)
	return err
}

// A question back to the requester settles the take until the requester answers
func (c cli) question(id string) error {
	if err := c.closeRelayOf(id); err != nil {
		return err
	}
	_, err := inbox.New(c.data).Question(id, c.session, c.now)
	return err
}

// How long to hold a request
// 1. An RFC 3339 time
// 2. A duration such as 1h or 90m
// 3. tomorrow for nine in the morning of the next day in local time
// 4. Empty to wait for the user
func holdUntil(s string, now time.Time) (time.Time, error) {
	switch s = strings.TrimSpace(s); s {
	case "":
		return time.Time{}, nil
	case "tomorrow":
		y, m, d := now.Local().AddDate(0, 0, 1).Date()
		return time.Date(y, m, d, 9, 0, 0, 0, time.Local), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(d), nil
	}
	return time.Time{}, fmt.Errorf("%w: --until is an RFC 3339 time, a duration such as 1h or tomorrow, not %q", errUsage, s)
}
