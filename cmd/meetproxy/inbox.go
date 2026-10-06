package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

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
	return exitCode(c.inboxAdd(it, f.key))
}

// The request the flags describe as the delegation d caught it
func (f flags) request(link string, d delegation.Delegation) (inbox.Item, error) {
	status, ok := map[string]inbox.Status{triage.Handle: inbox.StatusNew, triage.Ask: inbox.StatusAsk}[f.verdict]
	if !ok {
		return inbox.Item{}, fmt.Errorf("%w: inbox add needs --verdict handle or ask, not %q", errUsage, f.verdict)
	}
	return inbox.Item{
		Link: link, From: f.from, Ts: f.ts, Keywords: split(f.keywords), Name: f.name, Place: f.place,
		Skills: split(f.skills), Files: split(f.files), Status: status, Reason: f.reason,
		Delegation: d.Id, Task: d.Do, Target: f.target, MayApprove: d.MayApprove(f.self == "yes"),
	}, nil
}

// A request with a timestamp also moves the cursor of key past it
func (c cli) inboxAdd(it inbox.Item, key string) error {
	if _, err := parseLink(it.Link); err != nil {
		return err
	}
	store := inbox.New(c.data)
	stored, created, err := store.Add(it, c.now)
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

// Prints the link, the task, the target, whether a review may approve, the place and the suggested skills and files
func (c cli) inboxTake(id string) error {
	it, err := inbox.New(c.data).Take(id, c.session, c.now)
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
	fmt.Fprintf(c.out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.Link, task, orDash(it.Target), approve,
		orDash(it.Place), orDash(strings.Join(it.Skills, ",")), orDash(strings.Join(it.Files, ",")))
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
	it, ok, err := inbox.New(c.data).Claim(id, c.session, c.now)
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

func (c cli) hold(id string) error {
	if err := c.closeRelayOf(id); err != nil {
		return err
	}
	_, err := inbox.New(c.data).Hold(id, c.session, c.now)
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
func (c cli) closeRelayOf(id string) error {
	it, err := inbox.New(c.data).Get(id)
	if err != nil {
		return err
	}
	relays := relay.New(c.data)
	r, err := relays.Current(c.session)
	if errors.Is(err, relay.ErrNoOpen) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Origin == it.Link {
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
}

// Reads a message as JSON and prints what the matching delegation does with it
// key is mention, review-request or the id of a channel delegation
// Prints nothing when none matches so only a failure exits non zero
func (c cli) delegationMatch(key string) error {
	var m delegation.Message
	if err := json.NewDecoder(c.in).Decode(&m); err != nil {
		return fmt.Errorf("%w: message must be JSON: %w", errUsage, err)
	}
	ds, err := delegation.New(c.data).List()
	if err != nil {
		return err
	}
	d, ok := delegation.Pick(ds, key, m)
	if !ok {
		return nil
	}
	out := matched{Delegation: d.Id, Task: d.Do, Post: d.Post, Triage: d.Triaged(), Workspace: d.Workspace}
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
