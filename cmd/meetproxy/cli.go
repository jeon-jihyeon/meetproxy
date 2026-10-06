package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
	"github.com/jeon-jihyeon/meetproxy/internal/guard"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
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
	r, err := relay.New(c.data).Open(c.session, origin, target, c.now)
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
		fmt.Fprintf(c.out, "%d\t%s\t%s\t%s\n", cand.Score, cand.Name, cand.Abs(), strings.Join(cand.Topics, " | "))
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
	loc, err := parseLink(link)
	if err != nil {
		return exitFailed, err
	}
	r, err := relay.New(c.data).Current(c.session)
	if err != nil && !errors.Is(err, relay.ErrNoOpen) {
		return exitFailed, err
	}
	reason, err := denial(c.data, []dest.Location{loc}, r.Origin, r.Target)
	if err != nil {
		return exitFailed, err
	}
	return c.verdict(reason == "")
}

// Why posting to locs is denied while the relay of origin and target is open
// Empty origin and target mean no relay is open so only the allow list counts
func denial(data string, locs []dest.Location, origin, target string) (string, error) {
	o, _ := dest.Parse(origin)
	t, _ := dest.Parse(target)
	return guard.Decide(locs, o, t, dest.New(data).Allowed)
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
	Here bool `json:"here"`
}

func (c cli) tick(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("%w: tick needs --cwd", errUsage)
	}
	root, name := locmap.Place(cwd)
	items, err := inbox.New(c.data).Waiting(c.now)
	if err != nil {
		return err
	}
	rows := make([]waitingRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, waitingRow{
			Id: it.Id, Status: it.Status, Place: it.Place, Name: it.Name,
			Added: it.AddedAt.Unix(), Link: it.Link, Here: it.At(root, name),
		})
	}
	return json.NewEncoder(c.out).Encode(tick{Protocol: protocol, Place: root, Name: name, Waiting: rows})
}
