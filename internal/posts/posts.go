// Package posts keeps the replies meetproxy posted and the threads it watches for follow-ups
package posts

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

const (
	ModeInbox  = "inbox"  // posted for a request the user took from the inbox
	ModeManual = "manual" // posted from a relay the user opened by hand

	KindAnswer   = "answer"   // an answer to the request
	KindQuestion = "question" // a question back to the requester
)

const (
	// Posts older than this are dropped by Prune
	keepPosts = 30 * 24 * time.Hour
	// A thread is watched for follow-ups this long after the last post in it
	watchFor = 3 * 24 * time.Hour
	// Characters of a post body kept
	MaxBody = 4000
)

var (
	ErrNotFound = errors.New("no such post in the ledger")
	// Ids come from the command line so anything but an inbox id is rejected before it reaches a path
	validId = regexp.MustCompile(`^[0-9a-f]{12}$`)
	validTs = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// One reply meetproxy posted
type Post struct {
	// Inbox id of the origin
	Request string `json:"request"`
	Relay   string `json:"relay,omitempty"`
	// Where the request came from
	Origin string `json:"origin"`
	// Permalink of the reply itself
	Reply      string    `json:"reply"`
	Body       string    `json:"body"`
	Delegation string    `json:"delegation,omitempty"`
	Mode       string    `json:"mode"`
	Kind       string    `json:"kind"`
	Session    string    `json:"session,omitempty"`
	At         time.Time `json:"at"`
	// Set once the reply was edited away or deleted
	RetractedAt time.Time `json:"retracted_at,omitzero"`
}

// A thread read for replies after the last post in it
type Watch struct {
	Request string `json:"request"`
	Origin  string `json:"origin"`
	// Link of the thread the replies land in
	Thread string `json:"thread"`
	// Unix seconds of the newest reply already read
	Seen       string    `json:"seen"`
	Delegation string    `json:"delegation,omitempty"`
	Relay      string    `json:"relay,omitempty"`
	Until      time.Time `json:"until"`
}

type Store struct{ dir string }

func New(dataDir string) Store { return Store{dir: filepath.Join(dataDir, "posts")} }

// Records the post and watches its thread from the post on
// seen is the unix seconds of the post so only later replies count
func (s Store) Add(p Post, seen string, now time.Time) error {
	if !validId.MatchString(p.Request) || p.Reply == "" {
		return fmt.Errorf("a post needs a request id and a reply link, not %q and %q", p.Request, p.Reply)
	}
	if !validTs.MatchString(seen) {
		return fmt.Errorf("seen must be unix seconds %q", seen)
	}
	if r := []rune(p.Body); len(r) > MaxBody {
		p.Body = string(r[:MaxBody])
	}
	p.At = now.UTC()
	if err := s.append(p); err != nil {
		return err
	}
	w := Watch{Request: p.Request, Origin: p.Origin, Thread: p.Origin, Seen: seen, Delegation: p.Delegation, Relay: p.Relay, Until: now.Add(watchFor).UTC()}
	return fileio.WriteJSON(s.watchFile(p.Request), w)
}

func (s Store) append(p Post) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return fileio.AppendLine(s.ledger(), b)
}

// Newest first with the last record of each reply
// limit 0 lists every post
func (s Store) List(limit int) ([]Post, error) {
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	slices.Reverse(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Oldest first, one per reply
func (s Store) all() ([]Post, error) {
	f, err := os.Open(s.ledger())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Post
	at := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var p Post
		// A broken line only loses that post
		if json.Unmarshal(sc.Bytes(), &p) != nil {
			continue
		}
		if i, ok := at[p.Reply]; ok {
			out[i] = p
			continue
		}
		at[p.Reply] = len(out)
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, sc.Err()
}

// The post of a reply link
func (s Store) Find(reply string) (Post, error) {
	all, err := s.all()
	if err != nil {
		return Post{}, err
	}
	i := slices.IndexFunc(all, func(p Post) bool { return p.Reply == strings.TrimSpace(reply) })
	if i < 0 {
		return Post{}, fmt.Errorf("%w %q", ErrNotFound, reply)
	}
	return all[i], nil
}

// Marks a reply retracted by appending its record again
func (s Store) Retract(reply string, now time.Time) (Post, error) {
	p, err := s.Find(reply)
	if err != nil {
		return Post{}, err
	}
	p.RetractedAt = now.UTC()
	return p, s.append(p)
}

// Whether the request has a post that was not retracted
func (s Store) Posted(request string) (bool, error) {
	all, err := s.all()
	return slices.ContainsFunc(all, func(p Post) bool { return p.Request == request && p.RetractedAt.IsZero() }), err
}

// Threads still watched oldest first
// Expired ones are removed
func (s Store) Watches(now time.Time) ([]Watch, error) {
	entries, err := os.ReadDir(s.watchDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Watch
	for _, e := range entries {
		file := filepath.Join(s.watchDir(), e.Name())
		var w Watch
		if found, err := fileio.ReadJSON(file, &w); !found || err != nil || !now.Before(w.Until) {
			_ = os.Remove(file)
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out, nil
}

// Moves the watch of a request past the reply ts
// Only forward so an older read never makes a reply read twice
func (s Store) Seen(request, ts string) error {
	if !validId.MatchString(request) || !validTs.MatchString(ts) {
		return fmt.Errorf("watch seen needs a request id and unix seconds, not %q and %q", request, ts)
	}
	var w Watch
	found, err := fileio.ReadJSON(s.watchFile(request), &w)
	switch {
	case err != nil && found:
		return err
	case !found:
		return fmt.Errorf("%w: no watch of %s", ErrNotFound, request)
	}
	if !later(ts, w.Seen) {
		return nil
	}
	w.Seen = ts
	return fileio.WriteJSON(s.watchFile(request), w)
}

// Drops posts past keepPosts and records a later one replaced
func (s Store) Prune(now time.Time) error {
	_, err := fileio.RewriteLines(s.ledger(), func(lines [][]byte) [][]byte {
		last := map[string]int{}
		posts := make([]*Post, len(lines))
		for i, l := range lines {
			var p Post
			if json.Unmarshal(l, &p) != nil {
				continue
			}
			posts[i] = &p
			last[p.Reply] = i
		}
		var kept [][]byte
		for i, p := range posts {
			if p != nil && last[p.Reply] == i && now.Sub(p.At) <= keepPosts {
				kept = append(kept, lines[i])
			}
		}
		return kept
	})
	if err != nil {
		return err
	}
	_, err = s.Watches(now)
	return err
}

// Whether the unix seconds a come after b, compared as numbers of up to microseconds
func later(a, b string) bool {
	if b == "" {
		return true
	}
	return micros(a) > micros(b)
}

func micros(ts string) string {
	sec, frac, _ := strings.Cut(ts, ".")
	frac = (frac + "000000")[:6]
	return fmt.Sprintf("%020s%s", strings.TrimLeft(sec, "0"), frac)
}

func (s Store) ledger() string                  { return filepath.Join(s.dir, "posts.jsonl") }
func (s Store) watchDir() string                { return filepath.Join(s.dir, "watch") }
func (s Store) watchFile(request string) string { return filepath.Join(s.watchDir(), request+".json") }
