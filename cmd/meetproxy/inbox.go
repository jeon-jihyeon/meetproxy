package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/delegation"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
	"github.com/jeon-jihyeon/meetproxy/internal/triage"
)

func addRequest(c cli, args []string, f flags) (int, error) {
	d, err := c.delegationOf(f.delegation)
	if err != nil {
		return exitFailed, err
	}
	it, err := f.request(args[0], d)
	if err != nil {
		return exitCode(err)
	}
	perHour, dedupe := d.Limits()
	return exitCode(c.inboxAdd(it, inbox.Limits{PerHour: perHour, Dedupe: time.Duration(dedupe) * time.Minute}, f.key))
}

// Characters of the first line of a message the list shows
const summaryRunes = 200

// The request the flags describe as the delegation d caught it
func (f flags) request(link string, d delegation.Delegation) (inbox.Item, error) {
	for name, v := range map[string]string{"followup": f.followup, "correction": f.correction} {
		if !slices.Contains([]string{"", "yes", "no"}, v) {
			return inbox.Item{}, fmt.Errorf("%w: --%s is yes or no, not %q", errUsage, name, v)
		}
	}
	if !slices.Contains([]string{"", delegation.DepthQuick, delegation.DepthDeep}, f.depth) {
		return inbox.Item{}, fmt.Errorf("%w: --depth is quick or deep, not %q", errUsage, f.depth)
	}
	return inbox.Item{
		Thread: f.thread, Link: link, Source: f.source, From: f.from, Author: f.author, Channel: f.channel,
		Summary: summary(f.summary), Ts: f.ts, Reason: f.reason, Delegation: d.Id, Task: d.Do, Target: f.target,
		MayApprove: d.MayApprove(f.self == "yes"), Depth: f.depth, Followup: f.followup == "yes",
		Correction: f.correction == "yes", Digest: f.digest,
	}, nil
}

// The first line of a message cut to summaryRunes
func summary(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			if r := []rune(line); len(r) > summaryRunes {
				return string(r[:summaryRunes-1]) + "…"
			}
			return line
		}
	}
	return ""
}

// A request with a timestamp also moves the cursor of key past it
// One past the limits of its delegation is recorded as ignored and moves the cursor too
func (c cli) inboxAdd(it inbox.Item, lim inbox.Limits, key string) error {
	if _, err := parseLink(it.Link); err != nil {
		return err
	}
	store := inbox.New(c.data)
	stored, created, err := store.AddLimited(it, lim, c.now)
	if errors.Is(err, inbox.ErrLimited) || errors.Is(err, inbox.ErrDuplicate) {
		if err := store.Ignore(inbox.Ignored{Link: it.Link, From: it.From, Delegation: it.Delegation, Reason: err.Error()}, c.now); err != nil {
			return err
		}
		if it.Ts != "" {
			if err := store.Advance(key, it.Ts); err != nil {
				return err
			}
		}
		fmt.Fprintf(c.out, "%s\tignored\t%s\n", it.Link, err)
		return nil
	}
	if err != nil {
		return err
	}
	if it.Ts != "" {
		if err := store.Advance(key, it.Ts); err != nil {
			return err
		}
	}
	seen := "new"
	if !created {
		seen = "seen"
	}
	fmt.Fprintf(c.out, "%s\t%s\t%s\n", stored.Id, stored.Status, seen)
	return nil
}

// An empty id is the default delegation
func (c cli) delegationOf(id string) (delegation.Delegation, error) {
	if id == "" {
		return delegation.Default, nil
	}
	ds, err := delegation.New(c.data).List()
	if err != nil {
		return delegation.Delegation{}, err
	}
	i := slices.IndexFunc(ds, func(d delegation.Delegation) bool { return d.Id == id })
	if i < 0 {
		return delegation.Delegation{}, fmt.Errorf("no delegation %q", id)
	}
	return ds[i], nil
}

// Prints the link, the task, the target, whether a review may approve, the depth and the flags of a follow-up
func (c cli) inboxTake(id string) error {
	var it inbox.Item
	err := withScope(c.data, c.session, c.now, func() (err error) {
		it, err = inbox.New(c.data).Take(id, c.session, c.now)
		return err
	})
	if err != nil {
		return err
	}
	task := it.Task
	if task == "" {
		task = delegation.DoAnswer
	}
	approve := "no"
	if it.MayApprove {
		approve = "yes"
	}
	depth := it.Depth
	if depth == "" {
		depth = delegation.DepthQuick
	}
	var marks []string
	if it.Followup {
		marks = append(marks, "followup")
	}
	if it.Correction {
		marks = append(marks, "correction")
	}
	fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\t%s\n", it.Link, task, orDash(it.Target), approve, depth, orDash(strings.Join(marks, ",")))
	return nil
}

func (c cli) hold(id, until string) error {
	at, err := holdUntil(until, c.now)
	if err != nil {
		return err
	}
	if err := c.closeRelayOf(id); err != nil {
		return err
	}
	_, err = inbox.New(c.data).Hold(id, c.session, at, c.now)
	return err
}

func (c cli) done(id string) error {
	if err := c.closeRelayOf(id); err != nil {
		return err
	}
	_, err := inbox.New(c.data).Done(id, c.session, c.now)
	return err
}

// Keeps a relay open for another request
// A request this session took without a relay keeps its scope until the turn ends as a closed relay does
func (c cli) closeRelayOf(id string) error {
	it, err := inbox.New(c.data).Get(id)
	if err != nil {
		return err
	}
	relays := relay.New(c.data)
	r, err := relays.Current(c.session)
	switch {
	case errors.Is(err, relay.ErrNoOpen) && it.Status == inbox.StatusTaken && it.SessionId == c.session:
		return withScope(c.data, c.session, c.now, func() error { return relays.Linger(c.session, it.Link, it.Target, c.now) })
	case errors.Is(err, relay.ErrNoOpen):
		return nil
	case err != nil:
		return err
	case r.Origin == it.Link:
		_, err = relays.Close(c.session, c.now)
	}
	return err
}

// Prints the requests not done newest first
// 1. Each line is the id, the status, the source, the author, the link and the summary
// 2. Then when it was added and when its newest message was written in RFC 3339
// 3. Times come last so readers of the first six columns keep working
// 4. Statuses read as the list offers them so a held request whose time came is open
func (c cli) inboxList(limit int) error {
	items, err := inbox.New(c.data).List()
	if err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(it inbox.Item) bool { return it.Status == inbox.StatusDone })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	for _, it := range items {
		st := it.Status
		if it.Open(c.now) {
			st = inbox.StatusOpen
		}
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.Id, st, orDash(it.Source), orDash(it.Author), it.Link,
			orDash(it.Summary), it.AddedAt.UTC().Format(time.RFC3339), it.Last().UTC().Format(time.RFC3339))
	}
	return nil
}

// Prints where reading resumes
func (c cli) inboxCursor(key string) error {
	cur, err := inbox.New(c.data).Cursor(key, c.now)
	if err == nil {
		fmt.Fprintln(c.out, cur)
	}
	return err
}

func (c cli) inboxAdvance(key, ts string) error { return inbox.New(c.data).Advance(key, ts) }

func (c cli) pause(paused bool) error {
	if err := inbox.New(c.data).SetPaused(paused); err != nil {
		return err
	}
	if paused {
		fmt.Fprintln(c.out, "paused, no request is queued or taken until meetproxy resume")
	} else {
		fmt.Fprintln(c.out, "resumed")
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// With no engine prints the one set
func (c cli) triage(engine []string) error {
	store := triage.New(c.data)
	if len(engine) > 0 {
		return store.Save(triage.Config{Engine: triage.Engine(engine[0])})
	}
	cfg, err := store.Load()
	if err == nil {
		fmt.Fprintln(c.out, strings.TrimSpace(string(cfg.Engine)+" "+cfg.Command))
	}
	return err
}

func (c cli) triageCommand(line []string) error {
	return triage.New(c.data).Save(triage.Config{Engine: triage.EngineCommand, Command: strings.Join(line, " ")})
}

func (c cli) triagePrompt() error {
	in, err := c.triageInput()
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(c.out, triage.Prompt(in))
	return err
}

func (c cli) triageParse() error {
	b, err := io.ReadAll(c.in)
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(triage.Parse(string(b)))
}

func (c cli) triageRun() error {
	in, err := c.triageInput()
	if err != nil {
		return err
	}
	cfg, err := triage.New(c.data).Load()
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(triage.Run(cfg, in))
}

func (c cli) triageInput() (triage.Input, error) {
	var in triage.Input
	if err := json.NewDecoder(c.in).Decode(&in); err != nil {
		return triage.Input{}, fmt.Errorf("%w: triage input must be JSON: %w", errUsage, err)
	}
	return in, nil
}

func (c cli) delegations() error {
	ds, err := delegation.New(c.data).List()
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(ds)
}

func (c cli) delegationRemove(id string) error { return delegation.New(c.data).Remove(id) }

type matched struct {
	Delegation string `json:"delegation"`
	Task       string `json:"task"`
	Triage     bool   `json:"triage"`
	Target     string `json:"target,omitempty"`
	// quick or deep
	Depth string `json:"depth"`
	// What duplicates of the message share
	Digest string `json:"digest"`
	// The inbox already read the message so triage would only repeat
	Seen bool `json:"seen,omitempty"`
}

// Reads a message as JSON and prints what the matching delegation does with it
// 1. key is mention, review-request, dm, own-pr or the id of a channel delegation
// 2. With followup key is the id of the delegation that took the request before and its filters do not apply
// 3. With thread or ts it also says whether the session that held the lease until now read the message
// Prints nothing when none matches so only a failure exits non zero
func (c cli) delegationMatch(key string, followup bool, thread, ts string) error {
	var m delegation.Message
	if err := json.NewDecoder(c.in).Decode(&m); err != nil {
		return fmt.Errorf("%w: message must be JSON: %w", errUsage, err)
	}
	ds, err := delegation.New(c.data).List()
	if err != nil {
		return err
	}
	d, ok := delegation.Pick(ds, key, m)
	if followup {
		d, ok = delegation.ById(ds, key)
	}
	if !ok {
		return nil
	}
	seen := false
	if thread != "" || ts != "" {
		if seen, err = inbox.New(c.data).Known(thread, m.Link, ts, c.now); err != nil {
			return err
		}
	}
	return json.NewEncoder(c.out).Encode(matched{
		Delegation: d.Id, Task: d.Do, Triage: d.Triaged() || followup, Target: d.Target(m),
		Depth: delegation.Depth(m.Text), Digest: delegation.Digest(m.Text), Seen: seen,
	})
}

func (c cli) delegationPut() error {
	var d delegation.Delegation
	if err := json.NewDecoder(c.in).Decode(&d); err != nil {
		return fmt.Errorf("%w: delegation must be JSON: %w", errUsage, err)
	}
	return delegation.New(c.data).Put(d)
}
