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

// The request the flags describe as the delegation d caught it
// A sender outside the trust set is asked about whatever triage said since the request text may steer the session
// The user's own request is always trusted
func (f flags) request(link string, d delegation.Delegation) (inbox.Item, error) {
	status, ok := map[string]inbox.Status{triage.Handle: inbox.StatusNew, triage.Ask: inbox.StatusAsk}[f.verdict]
	if !ok {
		return inbox.Item{}, fmt.Errorf("%w: inbox add needs --verdict handle or ask, not %q", errUsage, f.verdict)
	}
	for name, v := range map[string]string{"trusted": f.trusted, "followup": f.followup, "correction": f.correction} {
		if !slices.Contains([]string{"", "yes", "no"}, v) {
			return inbox.Item{}, fmt.Errorf("%w: --%s is yes or no, not %q", errUsage, name, v)
		}
	}
	if !slices.Contains([]string{"", delegation.DepthQuick, delegation.DepthDeep}, f.depth) {
		return inbox.Item{}, fmt.Errorf("%w: --depth is quick or deep, not %q", errUsage, f.depth)
	}
	reason := f.reason
	if status == inbox.StatusNew && f.self != "yes" && !d.Trusts(f.from, f.trusted == "yes") {
		status, reason = inbox.StatusAsk, "sender outside the trust set"
	}
	return inbox.Item{
		Link: link, From: f.from, Ts: f.ts, Keywords: split(f.keywords), Name: f.name, Place: f.place,
		Skills: split(f.skills), Files: split(f.files), Status: status, Reason: reason,
		Delegation: d.Id, Task: d.Do, Target: f.target, MayApprove: d.MayApprove(f.self == "yes"),
		Depth: f.depth, Followup: f.followup == "yes", Correction: f.correction == "yes", Digest: f.digest,
		Priority: d.Priority, Handoff: d.Handoff,
	}, nil
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
		fmt.Fprintf(c.out, "%s\tignored\t%s\t%s\n", inbox.IdOf(it.Link), orDash(it.Name), err)
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
	fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\n", stored.Id, stored.Status, orDash(stored.Name), seen)
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

// Prints the link, the task, the target, whether a review may approve, the place, the suggested skills and files,
// the depth and the flags of a follow-up
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
	fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.Link, task, orDash(it.Target), approve,
		orDash(it.Place), orDash(strings.Join(it.Skills, ",")), orDash(strings.Join(it.Files, ",")), depth, orDash(strings.Join(marks, ",")))
	return nil
}

// Prints the link like take, or nothing when the session is busy
// A session with an open relay is busy as well as one still working on a request it took
func (c cli) inboxClaim(id string) error {
	_, err := relay.New(c.data).Current(c.session)
	if err == nil {
		return nil
	}
	if !errors.Is(err, relay.ErrNoOpen) {
		return err
	}
	var it inbox.Item
	var ok bool
	err = withScope(c.data, c.session, c.now, func() (err error) {
		it, ok, err = inbox.New(c.data).Claim(id, c.session, c.now)
		return err
	})
	if err != nil || !ok {
		return err
	}
	fmt.Fprintln(c.out, it.Link)
	return nil
}

// Each settle closes the relay of the request before its status changes
// A failed close then leaves the status as it was so the same command runs again
func (c cli) ask(id, reason string) error {
	if err := c.closeRelayOf(id); err != nil {
		return err
	}
	_, err := inbox.New(c.data).Ask(id, c.session, reason, c.now)
	return err
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

func (c cli) inboxList(limit int) error {
	items, err := inbox.New(c.data).List()
	if err != nil {
		return err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	for _, it := range items {
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\n", it.Id, it.Status, orDash(it.Name), it.Link, it.Reason)
	}
	return nil
}

func (c cli) inboxWaiting() error {
	items, err := inbox.New(c.data).Waiting(c.now)
	for _, it := range items {
		fmt.Fprintf(c.out, "%s\t%s\t%s\t%d\t%s\n", it.Id, it.Status, orDash(it.Name), it.AddedAt.Unix(), it.Link)
	}
	return err
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
	Post       string `json:"post"`
	Triage     bool   `json:"triage"`
	Target     string `json:"target,omitempty"`
	// Empty when the work map has to pick it
	Workspace string `json:"workspace,omitempty"`
	// quick or deep
	Depth string `json:"depth"`
	// What duplicates of the message share
	Digest string `json:"digest"`
}

// Reads a message as JSON and prints what the matching delegation does with it
// 1. key is mention, review-request, dm, own-pr or the id of a channel delegation
// 2. With followup key is the id of the delegation that took the request before and its filters do not apply
// Prints nothing when none matches so only a failure exits non zero
func (c cli) delegationMatch(key string, followup bool) error {
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
	out := matched{
		Delegation: d.Id, Task: d.Do, Post: d.PostFor(m), Triage: d.Triaged() || followup, Workspace: d.Workspace,
		Depth: delegation.Depth(m.Text), Digest: delegation.Digest(m.Text),
	}
	var repo string
	out.Target, repo = d.Target(m)
	if out.Workspace == "" {
		out.Workspace = repo
	}
	return json.NewEncoder(c.out).Encode(out)
}

func (c cli) delegationPut() error {
	var d delegation.Delegation
	if err := json.NewDecoder(c.in).Decode(&d); err != nil {
		return fmt.Errorf("%w: delegation must be JSON: %w", errUsage, err)
	}
	return delegation.New(c.data).Put(d)
}
