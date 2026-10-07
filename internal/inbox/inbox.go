// Package inbox queues requests from any source until one session takes each
package inbox

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type Status string

const (
	StatusNew   Status = "new"   // a session may answer it on its own
	StatusAsk   Status = "ask"   // a session asks the user before answering
	StatusHeld  Status = "held"  // the user put it off and runs handle when ready
	StatusTaken Status = "taken" // claimed by a session
	StatusDone  Status = "done"  // answered or dropped
)

type Item struct {
	Id       string   `json:"id"`
	Link     string   `json:"link"`
	From     string   `json:"from,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	// Display name of the place
	Name string `json:"name,omitempty"`
	// Absolute root of the place the request belongs to
	Place string `json:"place,omitempty"`
	// Skills the work map suggests starting from
	Skills []string `json:"skills,omitempty"`
	// Files the work map suggests starting from
	Files  []string `json:"files,omitempty"`
	Status Status   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	// Delegation that caught the message
	Delegation string `json:"delegation,omitempty"`
	// Task the delegation names
	Task string `json:"task,omitempty"`
	// What the task works on when it is not the message itself such as a pull request
	Target     string `json:"target,omitempty"`
	MayApprove bool   `json:"may_approve,omitempty"`
	// Unix seconds of the newest message that asked
	// A newer one on a done request asks again
	Ts        string    `json:"ts,omitempty"`
	SessionId string    `json:"session_id,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Last sign that the session working on a take is still at it
	// Kept apart from UpdatedAt so a heartbeat never reads as a change of status
	HeartbeatAt time.Time `json:"heartbeat_at,omitzero"`
}

// No session works on it
// A take with no sign of work for busyFor counts as abandoned
func (it Item) Waiting(now time.Time) bool {
	switch it.Status {
	case StatusNew, StatusAsk, StatusHeld:
		return true
	case StatusTaken:
		return now.Sub(it.lastSeen()) >= busyFor
	default:
		return false
	}
}

// The later of the last change and the last heartbeat
func (it Item) lastSeen() time.Time {
	if it.HeartbeatAt.After(it.UpdatedAt) {
		return it.HeartbeatAt
	}
	return it.UpdatedAt
}

// Whether the request belongs to the place with this root and name
// The root identifies a place so the name only counts for a request stored without one
func (it Item) At(root, name string) bool {
	if it.Place != "" {
		return it.Place == root
	}
	return it.Name != "" && it.Name == name
}

type Store struct{ dir string }

func New(dataDir string) Store { return Store{dir: filepath.Join(dataDir, "inbox")} }

var (
	ErrNotFound = errors.New("no such request")
	ErrCorrupt  = errors.New("request file is corrupt")
	ErrPaused   = errors.New("meetproxy is paused, run meetproxy resume")
	ErrTaken    = errors.New("another session works on the request")
)

// Ids come from the command line so anything but IdOf output is rejected before it reaches a path
var validId = regexp.MustCompile(`^[0-9a-f]{12}$`)

// The same link always maps to the same id so a request found twice is stored once
func IdOf(link string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(link)))
	return hex.EncodeToString(sum[:6])
}

// A done request is removed after this so the inbox only holds what is still useful
const keepDone = 7 * 24 * time.Hour

// Returns false with the stored item when the link was seen before
// 1. A done request asked again with a newer timestamp is stored anew and returns true
// 2. A request still open keeps the newest timestamp so an older scan never reopens it once done
func (s Store) Add(it Item, now time.Time) (Item, bool, error) {
	if s.Paused() {
		return Item{}, false, ErrPaused
	}
	if it.Ts != "" && !validTs.MatchString(it.Ts) {
		return Item{}, false, fmt.Errorf("timestamp must be unix seconds %q", it.Ts)
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, false, err
	}
	defer unlock()
	s.prune(now)
	it.Id = IdOf(it.Link)
	it.AddedAt, it.UpdatedAt = now.UTC(), now.UTC()
	seen, err := s.Get(it.Id)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Item{}, false, err
	case !newer(it.Ts, seen.Ts):
		return seen, false, nil
	case seen.Status != StatusDone:
		seen.Ts = it.Ts
		return seen, false, s.put(seen)
	}
	return it, true, s.put(it)
}

func (s Store) Get(id string) (Item, error) {
	if !validId.MatchString(id) {
		return Item{}, fmt.Errorf("%w %q", ErrNotFound, id)
	}
	var it Item
	found, err := fileio.ReadJSON(s.itemFile(id), &it)
	switch {
	case err != nil && found:
		return Item{}, fmt.Errorf("%w: %s: %w", ErrCorrupt, s.itemFile(id), err)
	case err != nil:
		return Item{}, err
	case !found:
		return Item{}, fmt.Errorf("%w %s", ErrNotFound, id)
	}
	return it, nil
}

// Newest first
// 1. A request removed while the list is read is left out
// 2. A request file that cannot be read is moved aside so one broken file never stops every session
func (s Store) List() ([]Item, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		it, err := s.Get(id)
		switch {
		case errors.Is(err, ErrNotFound):
			continue
		case errors.Is(err, ErrCorrupt):
			_, _ = fileio.Quarantine(s.itemFile(id))
			continue
		case err != nil:
			return nil, err
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.After(out[j].AddedAt) })
	return out, nil
}

// Every request no session works on, oldest first
// One a session took and left unsettled comes back as ask since its automatic attempt failed
func (s Store) Waiting(now time.Time) ([]Item, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Item
	for i := len(all) - 1; i >= 0; i-- {
		it := all[i]
		if !it.Waiting(now) {
			continue
		}
		if it.Status == StatusTaken {
			it.Status, it.SessionId = StatusAsk, ""
		}
		out = append(out, it)
	}
	return out, nil
}

// Best effort since a leftover done request only takes space
// Runs under the lock so a request asked again is never removed
func (s Store) prune(now time.Time) {
	all, err := s.List()
	if err != nil {
		return
	}
	for _, it := range all {
		if it.Status == StatusDone && now.Sub(it.UpdatedAt) > keepDone {
			_ = os.Remove(s.itemFile(it.Id))
		}
	}
}

// A taken request with no sign of work for this long no longer keeps its session busy and any session may take it over
// A running turn renews it through Touch so only a session that stopped working loses its take
const busyFor = 20 * time.Minute

// Touch writes a heartbeat at most this often so a busy session costs few writes
const touchEvery = 5 * time.Minute

// Requests nobody took are closed after these
const (
	expireOpen = 14 * 24 * time.Hour
	expireHeld = 30 * 24 * time.Hour
)

// Takes a request for a session that works on one request at a time
// 1. Returns false when the session still works on one it took
// 2. A held request waits for the user so only Take reaches it
// 3. Which request a session should take is decided by the caller from Waiting
func (s Store) Claim(id, sessionId string, now time.Time) (Item, bool, error) {
	if s.Paused() {
		return Item{}, false, ErrPaused
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, false, err
	}
	defer unlock()
	_, busy, err := s.TakenBy(sessionId, now)
	if err != nil || busy {
		return Item{}, false, err
	}
	it, err := s.take(id, sessionId, now, StatusNew, StatusAsk)
	return it, err == nil, err
}

// The request the session took and still works on
func (s Store) TakenBy(sessionId string, now time.Time) (Item, bool, error) {
	all, err := s.List()
	if err != nil {
		return Item{}, false, err
	}
	for _, it := range all {
		if it.Status == StatusTaken && it.SessionId == sessionId && !it.Waiting(now) {
			return it, true, nil
		}
	}
	return Item{}, false, nil
}

// Turns a request the session could not settle into one the user is asked about
func (s Store) Ask(id, sessionId, reason string, now time.Time) (Item, error) {
	return s.release(id, sessionId, StatusAsk, reason, now, StatusTaken, StatusNew)
}

// Puts a request off until the user runs handle on it
func (s Store) Hold(id, sessionId string, now time.Time) (Item, error) {
	return s.release(id, sessionId, StatusHeld, "", now, StatusTaken, StatusAsk)
}

func (s Store) Done(id, sessionId string, now time.Time) (Item, error) {
	return s.release(id, sessionId, StatusDone, "", now, StatusNew, StatusAsk, StatusHeld, StatusTaken, StatusDone)
}

// Frees the request so any session can take it again
// 1. A request another session still works on stays with it so a session never settles a take it lost
// 2. An empty reason keeps the one stored
func (s Store) release(id, sessionId string, to Status, reason string, now time.Time, from ...Status) (Item, error) {
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()
	it, err := s.Get(id)
	if err != nil {
		return Item{}, err
	}
	if !slices.Contains(from, it.Status) {
		return Item{}, fmt.Errorf("request %s is %s", id, it.Status)
	}
	if it.takenByOther(sessionId, now) {
		return Item{}, fmt.Errorf("%w %s", ErrTaken, id)
	}
	it.Status, it.SessionId, it.UpdatedAt = to, "", now.UTC()
	if reason != "" {
		it.Reason = reason
	}
	return it, s.put(it)
}

// Only one session takes a request since the check and the write happen under the lock
// 1. Taking again from the same session returns the item
// 2. A held request is taken too since the user names it
func (s Store) Take(id, sessionId string, now time.Time) (Item, error) {
	if s.Paused() {
		return Item{}, ErrPaused
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()
	return s.take(id, sessionId, now, StatusNew, StatusAsk, StatusHeld)
}

// Takes a request in one of the from statuses or a take no session keeps busy
// Callers hold the lock
func (s Store) take(id, sessionId string, now time.Time, from ...Status) (Item, error) {
	it, err := s.Get(id)
	if err != nil {
		return Item{}, err
	}
	switch {
	case it.Status == StatusTaken && it.takenByOther(sessionId, now):
		return Item{}, fmt.Errorf("%w %s", ErrTaken, id)
	case it.Status != StatusTaken && !slices.Contains(from, it.Status):
		return Item{}, fmt.Errorf("request %s is %s", id, it.Status)
	}
	it.Status, it.SessionId, it.UpdatedAt = StatusTaken, sessionId, now.UTC()
	return it, s.put(it)
}

// Another session holds a take that still keeps it busy
func (it Item) takenByOther(sessionId string, now time.Time) bool {
	return it.Status == StatusTaken && it.SessionId != sessionId && !it.Waiting(now)
}

// Does nothing for a link that never came through the inbox
func (s Store) DoneByLink(link, sessionId string, now time.Time) error {
	_, err := s.Done(IdOf(link), sessionId, now)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Unix seconds of the newest message checked under a key such as a delegation id
// The first read starts the key at now so nothing before it is ever read and nothing after it is skipped
func (s Store) Cursor(key string, now time.Time) (string, error) {
	if s.Paused() {
		return "", ErrPaused
	}
	file, err := s.cursorFile(key)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.Advance(key, strconv.FormatInt(now.Unix(), 10)); err != nil {
			return "", err
		}
		b, err = os.ReadFile(file)
	}
	return strings.TrimSpace(string(b)), err
}

// Unix seconds as Slack writes them
var validTs = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// Whether the unix seconds ts come after than
// 1. Compared as digits since a float drops the microseconds of a Slack timestamp
// 2. Any valid ts comes after an empty or malformed than
// 3. An empty or malformed ts never comes after anything
func newer(ts, than string) bool {
	if !validTs.MatchString(ts) {
		return false
	}
	if !validTs.MatchString(than) {
		return true
	}
	sec, frac, _ := strings.Cut(ts, ".")
	thanSec, thanFrac, _ := strings.Cut(than, ".")
	sec, thanSec = strings.TrimLeft(sec, "0"), strings.TrimLeft(thanSec, "0")
	if c := cmp.Compare(len(sec), len(thanSec)); c != 0 {
		return c > 0
	}
	if c := strings.Compare(sec, thanSec); c != 0 {
		return c > 0
	}
	width := max(len(frac), len(thanFrac))
	return padRight(frac, width) > padRight(thanFrac, width)
}

func padRight(s string, width int) string { return s + strings.Repeat("0", width-len(s)) }

// Only moves forward so an older timestamp never makes a message checked twice
func (s Store) Advance(key, ts string) error {
	if !validTs.MatchString(ts) {
		return fmt.Errorf("timestamp must be unix seconds %q", ts)
	}
	file, err := s.cursorFile(key)
	if err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if b, err := os.ReadFile(file); err == nil && !newer(ts, strings.TrimSpace(string(b))) {
		return nil
	}
	return fileio.WriteAtomic(file, []byte(ts), 0o600)
}

func (s Store) Paused() bool {
	_, err := os.Stat(s.pauseFile())
	return err == nil
}

func (s Store) SetPaused(paused bool) error {
	if !paused {
		if err := os.Remove(s.pauseFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return fileio.WriteAtomic(s.pauseFile(), nil, 0o600)
}

// Serializes every read then write across the sessions that share the inbox
func (s Store) lock() (unlock func(), err error) { return fileio.Lock(filepath.Join(s.dir, "lock")) }

func (s Store) put(it Item) error { return fileio.WriteJSON(s.itemFile(it.Id), it) }

func (s Store) itemFile(id string) string { return filepath.Join(s.dir, id+".json") }
func (s Store) pauseFile() string         { return filepath.Join(s.dir, "paused") }

var validKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// The mention cursor keeps the file name it had before keys
func (s Store) cursorFile(key string) (string, error) {
	switch {
	case key == "" || key == "mention":
		return filepath.Join(s.dir, "cursor"), nil
	case validKey.MatchString(key):
		return filepath.Join(s.dir, "cursor-"+key), nil
	default:
		return "", fmt.Errorf("cursor key must be lowercase letters, digits and dashes %q", key)
	}
}

// Number of request files List moved aside
func (s Store) Corrupt() (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "corrupt"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return len(entries), err
}

// Renews the takes of a session that is still working so they do not age into ask
func (s Store) Touch(sessionId string, now time.Time) error {
	stale := func(it Item) bool {
		return it.Status == StatusTaken && it.SessionId == sessionId && now.Sub(it.lastSeen()) >= touchEvery
	}
	all, err := s.List()
	if err != nil || !slices.ContainsFunc(all, stale) {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	for _, it := range all {
		// Read again under the lock since another session may have settled it meanwhile
		cur, err := s.Get(it.Id)
		if err != nil || !stale(cur) {
			continue
		}
		cur.HeartbeatAt = now.UTC()
		if err := s.put(cur); err != nil {
			return err
		}
	}
	return nil
}

// Closes requests left waiting so long that answering them would no longer help
// 1. new and ask past expireOpen
// 2. held past expireHeld since the user put it off on purpose
func (s Store) Expire(now time.Time) (int, error) {
	unlock, err := s.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	all, err := s.List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range all {
		age := now.Sub(it.UpdatedAt)
		switch {
		case (it.Status == StatusNew || it.Status == StatusAsk) && age > expireOpen:
		case it.Status == StatusHeld && age > expireHeld:
		default:
			continue
		}
		it.Status, it.SessionId, it.Reason, it.UpdatedAt = StatusDone, "", "expired", now.UTC()
		if err := s.put(it); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Which session reads a source until when
type lease struct {
	Session string    `json:"session"`
	Until   time.Time `json:"until"`
}

// Holds the lease of a source for ttl and reports false while another session holds it
// The check and the write run under the inbox lock so two sessions never both hold it
// Holding again renews it
func (s Store) Lease(source, sessionId string, ttl time.Duration, now time.Time) (bool, error) {
	file, err := s.leaseFile(source)
	if err != nil {
		return false, err
	}
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	var cur lease
	if _, err := fileio.ReadJSON(file, &cur); err == nil && cur.Session != sessionId && cur.Until.After(now) {
		return false, nil
	}
	return true, fileio.WriteJSON(file, lease{sessionId, now.Add(ttl).UTC()})
}

// Gives up the lease so another session can read the source at once
// A lease another session took over meanwhile stays with it
func (s Store) Drop(source, sessionId string) error {
	file, err := s.leaseFile(source)
	if err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	var cur lease
	if found, err := fileio.ReadJSON(file, &cur); !found || (err == nil && cur.Session != sessionId) {
		return nil
	}
	err = os.Remove(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) leaseFile(source string) (string, error) {
	if !validKey.MatchString(source) {
		return "", fmt.Errorf("source must be lowercase letters, digits and dashes %q", source)
	}
	return filepath.Join(s.dir, "lease-"+source+".json"), nil
}
