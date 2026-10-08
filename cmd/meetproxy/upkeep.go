package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/inbox"
	"github.com/jeon-jihyeon/meetproxy/internal/posts"
	"github.com/jeon-jihyeon/meetproxy/internal/relay"
)

const (
	// Three ticks of the mod so one slow check never loses the source
	defaultLeaseTTL = 3 * time.Minute
	// Hook failures status keeps
	keptHookFailures = 10
	tidyEvery        = 24 * time.Hour
)

var validSource = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Exits 0 when this session holds the lease and 1 when another does
func (c cli) leaseHold(source string, ttl time.Duration) (int, error) {
	if ttl <= 0 {
		ttl = defaultLeaseTTL
	}
	held, err := inbox.New(c.data).Lease(source, c.session, ttl, c.now)
	if err != nil {
		return exitFailed, err
	}
	if !held {
		fmt.Fprintln(c.out, "held by another session")
		return exitFailed, nil
	}
	fmt.Fprintln(c.out, "held")
	return 0, nil
}

func (c cli) leaseDrop(source string) error { return inbox.New(c.data).Drop(source, c.session) }

// What the last check of a source found
type sourceHealth struct {
	Ok    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Found int       `json:"found"`
	At    time.Time `json:"at"`
}

type hookFailure struct {
	Hook  string    `json:"hook"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// Kept so status shows what broke without the user reading logs
type health struct {
	Sources map[string]sourceHealth `json:"sources"`
	// Newest last
	Hooks []hookFailure `json:"hooks,omitempty"`
}

func healthFile(data string) string { return filepath.Join(data, "health.json") }

// Every session writes it so each change reads and writes under its own lock
func updateHealth(data string, change func(h *health)) error {
	unlock, err := fileio.Lock(filepath.Join(data, "health.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	h := readHealth(data)
	change(&h)
	return fileio.WriteJSON(healthFile(data), h)
}

// A file that cannot be read starts over since it only reports
func readHealth(data string) health {
	var h health
	if found, err := fileio.ReadJSON(healthFile(data), &h); !found || err != nil {
		h = health{}
	}
	if h.Sources == nil {
		h.Sources = map[string]sourceHealth{}
	}
	return h
}

// A record the same but for its time is rewritten only this often so a tick of every session writes nothing
const healthEvery = 10 * time.Minute

// Reads {ok, error, found} on stdin
func (c cli) recordHealth(source string) error {
	if !validSource.MatchString(source) {
		return fmt.Errorf("%w: source must be lowercase letters, digits and dashes %q", errUsage, source)
	}
	var in sourceHealth
	if err := json.NewDecoder(c.in).Decode(&in); err != nil {
		return fmt.Errorf("%w: health must be JSON: %w", errUsage, err)
	}
	in.At = c.now.UTC()
	// Read without the lock since a stale read only costs one more write
	if cur, ok := readHealth(c.data).Sources[source]; ok && cur.sameAs(in) && c.now.Sub(cur.At) < healthEvery {
		return nil
	}
	return updateHealth(c.data, func(h *health) { h.Sources[source] = in })
}

func (h sourceHealth) sameAs(o sourceHealth) bool {
	return h.Ok == o.Ok && h.Error == o.Error && h.Found == o.Found
}

func recordHookFailure(data, hook string, failure error, now time.Time) error {
	return updateHealth(data, func(h *health) {
		h.Hooks = append(h.Hooks, hookFailure{hook, failure.Error(), now.UTC()})
		if len(h.Hooks) > keptHookFailures {
			h.Hooks = h.Hooks[len(h.Hooks)-keptHookFailures:]
		}
	})
}

type inboxCounts struct {
	Open     int `json:"open"`
	Taken    int `json:"taken"`
	Held     int `json:"held"`
	Question int `json:"question"`
	Corrupt  int `json:"corrupt"`
}

// A held request and when it is asked about again
type heldRow struct {
	Id      string `json:"id"`
	Summary string `json:"summary,omitempty"`
	Link    string `json:"link"`
	// Zero waits for the user
	Until time.Time `json:"until,omitzero"`
}

// Records status counts within the windows they are kept for
type history struct {
	// Replies in the ledger of the last 30 days
	Posts int `json:"posts"`
	// Of them retracted
	Retracted int `json:"retracted"`
	// Messages not queued in the last 3 days
	Ignored int `json:"ignored"`
	// Threads read for follow-ups
	Watching int `json:"watching"`
}

type sizes struct {
	Closed int64 `json:"closed"`
}

type status struct {
	Protocol   int                     `json:"protocol"`
	Version    string                  `json:"version"`
	Paused     bool                    `json:"paused"`
	Slack      slackState              `json:"slack"`
	Sources    map[string]sourceHealth `json:"sources"`
	Hooks      []hookFailure           `json:"hooks,omitempty"`
	Inbox      inboxCounts             `json:"inbox"`
	Held       []heldRow               `json:"held"`
	History    history                 `json:"history"`
	RelaysOpen int                     `json:"relays_open"`
	Bytes      sizes                   `json:"bytes"`
}

// Everything the status skill shows read from disk alone
func (c cli) status() error {
	store := inbox.New(c.data)
	items, err := store.List()
	if err != nil {
		return err
	}
	var counts inboxCounts
	held := []heldRow{}
	for _, it := range items {
		switch {
		case it.Open(c.now):
			counts.Open++
		case it.Status == inbox.StatusHeld:
			counts.Held++
			held = append(held, heldRow{it.Id, it.Summary, it.Link, it.HeldUntil})
		case it.Status == inbox.StatusQuestion:
			counts.Question++
		case it.Status == inbox.StatusTaken:
			counts.Taken++
		}
	}
	if counts.Corrupt, err = store.Corrupt(); err != nil {
		return err
	}
	hist, err := c.history(store)
	if err != nil {
		return err
	}
	h := readHealth(c.data)
	open, _ := os.ReadDir(filepath.Join(c.data, "relay", "open"))
	return json.NewEncoder(c.out).Encode(status{
		Protocol: protocol, Version: version, Paused: store.Paused(), Slack: readSlackState(c.data, true),
		Sources: h.Sources, Hooks: h.Hooks, Inbox: counts, Held: held, History: hist, RelaysOpen: len(open),
		Bytes: sizes{Closed: size(filepath.Join(c.data, "relay", "closed.jsonl"))},
	})
}

func (c cli) history(store inbox.Store) (history, error) {
	ledger := posts.New(c.data)
	ps, err := ledger.List(0)
	if err != nil {
		return history{}, err
	}
	ignored, err := store.IgnoredSince(c.now, 0)
	if err != nil {
		return history{}, err
	}
	watch, err := ledger.Watches(c.now)
	h := history{Posts: len(ps), Ignored: len(ignored), Watching: len(watch)}
	for _, p := range ps {
		if !p.RetractedAt.IsZero() {
			h.Retracted++
		}
	}
	return h, err
}

func size(file string) int64 {
	info, err := os.Stat(file)
	if err != nil {
		return 0
	}
	return info.Size()
}

// When tidy last ran and whether the data tree was made private
type tidyState struct {
	At         time.Time `json:"at"`
	Restricted bool      `json:"restricted"`
}

// Removes what only grows
// 1. With daily it runs once a day and every session start may ask for it
// 2. A tidy another session is running already covers this one
// 3. The first run makes files written before 0.1.6 private
func (c cli) tidy(daily bool) error {
	unlock, ok, err := fileio.TryLock(filepath.Join(c.data, "tidy.lock"))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	file := filepath.Join(c.data, "tidy.json")
	var st tidyState
	if _, err := fileio.ReadJSON(file, &st); err != nil {
		st = tidyState{}
	}
	// Every session start runs this so state from before markers gets its marker directory soon
	if err := ensureScopeDir(c.data, c.now); err != nil {
		return err
	}
	if daily && c.now.Sub(st.At) < tidyEvery {
		return nil
	}
	if !st.Restricted {
		if err := fileio.Restrict(c.data); err != nil {
			return err
		}
		st.Restricted = true
	}
	expired, err := inbox.New(c.data).Expire(c.now)
	if err != nil {
		return err
	}
	if err := inbox.New(c.data).Prune(c.now); err != nil {
		return err
	}
	// The location map is gone and its file held request topics so it leaves too
	err = os.Remove(filepath.Join(c.data, "map.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	err = errors.Join(err, relay.New(c.data).Prune(c.now), pruneScope(c.data, c.now),
		inbox.New(c.data).PruneIgnored(c.now), posts.New(c.data).Prune(c.now))
	if err != nil {
		return err
	}
	st.At = c.now.UTC()
	if err := fileio.WriteJSON(file, st); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "tidied, %d requests expired\n", expired)
	return nil
}
