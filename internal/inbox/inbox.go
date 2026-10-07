// Package inbox keeps requests from any source until the user takes one up in a session
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
	StatusOpen  Status = "open"  // nobody works on it
	StatusHeld  Status = "held"  // the user put it off until a time or until they take it
	StatusTaken Status = "taken" // a session works on it
	StatusDone  Status = "done"  // answered or dropped
	// asked the requester back and waits for the reply
	StatusQuestion Status = "question"
)

// Statuses from before the inbox waited for the user
// Both mean nobody works on the request
var legacyOpen = []Status{"new", "ask"}

// Reason a question left unanswered for questionFor opens again with
const Unanswered = "requester did not answer"

// Reason a request opens again with when a newer message came while it was taken
const Replied = "the requester wrote again while the request was handled"

// Reason a take opens again with when its session ended or kept it past takeFor
const Released = "the session that took it ended"

// Reactions a request may be acknowledged with
const (
	AckSeen = "eyes" // the request was queued
	AckDone = "done" // the request was answered
)

type Item struct {
	Id string `json:"id"`
	// Key of the thread the request sits in
	// Every message of one thread is one request
	Thread string `json:"thread,omitempty"`
	// Link of the first message that asked, where the reply goes
	Link   string `json:"link"`
	Source string `json:"source,omitempty"`
	// Author id
	From string `json:"from,omitempty"`
	// Author display name
	Author  string `json:"author,omitempty"`
	Channel string `json:"channel,omitempty"`
	// First line of the newest message as the list shows it
	// Untrusted text
	Summary string `json:"summary,omitempty"`
	Status  Status `json:"status"`
	Reason  string `json:"reason,omitempty"`
	// Delegation that caught the message
	Delegation string `json:"delegation,omitempty"`
	// Task the delegation names
	Task string `json:"task,omitempty"`
	// What the task works on when it is not the message itself such as a pull request
	Target     string `json:"target,omitempty"`
	MayApprove bool   `json:"may_approve,omitempty"`
	// Unix seconds of the newest message that asked
	// A newer one on a settled request opens it again
	Ts        string    `json:"ts,omitempty"`
	SessionId string    `json:"session_id,omitempty"`
	AddedAt   time.Time `json:"added_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// When a held request opens again
	// Zero waits until the user takes it
	HeldUntil time.Time `json:"held_until,omitzero"`
	// quick or deep as the request asked
	Depth string `json:"depth,omitempty"`
	// A reply in a thread meetproxy already answered
	Followup bool `json:"followup,omitempty"`
	// The reply says the earlier answer was wrong
	Correction bool `json:"correction,omitempty"`
	// Digest of the request text that duplicates are told by
	Digest string `json:"digest,omitempty"`
	// Reactions already added
	Acked []string `json:"acked,omitempty"`
	// A newer message came while a session worked on the request
	// The request opens again once the take settles so the message is never absorbed
	Again bool `json:"again,omitempty"`
}

// How long a question to the requester waits before it opens again
const questionFor = 3 * 24 * time.Hour

// A held request whose time came
func (it Item) Due(now time.Time) bool {
	return it.Status == StatusHeld && !it.HeldUntil.IsZero() && !now.Before(it.HeldUntil)
}

// A question the requester left unanswered for questionFor
func (it Item) Unanswered(now time.Time) bool {
	return it.Status == StatusQuestion && now.Sub(it.UpdatedAt) >= questionFor
}

// No session works on it
// A take kept past takeFor counts as abandoned
func (it Item) Waiting(now time.Time) bool {
	switch it.Status {
	case StatusOpen, StatusHeld, StatusQuestion:
		return true
	case StatusTaken:
		return now.Sub(it.UpdatedAt) >= takeFor
	default:
		return false
	}
}

// Whether the list offers it to the user now
// A request put off or waiting for the requester comes back once its time came
func (it Item) Open(now time.Time) bool {
	switch it.Status {
	case StatusOpen:
		return true
	case StatusHeld:
		return it.Due(now)
	case StatusQuestion:
		return it.Unanswered(now)
	case StatusTaken:
		return it.Waiting(now)
	default:
		return false
	}
}

type Store struct{ dir string }

func New(dataDir string) Store { return Store{dir: filepath.Join(dataDir, "inbox")} }

var (
	ErrNotFound = errors.New("no such request")
	ErrCorrupt  = errors.New("request file is corrupt")
	ErrPaused   = errors.New("meetproxy is paused, run meetproxy resume")
	ErrTaken    = errors.New("another session works on the request until it settles it or ends")
)

// Ids come from the command line so anything but IdOf output is rejected before it reaches a path
var validId = regexp.MustCompile(`^[0-9a-f]{12}$`)

// The same key always maps to the same id so a request found twice is stored once
func IdOf(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:6])
}

// The id of an item by its thread or its link when it names no thread
func (it Item) key() string {
	if it.Thread != "" {
		return it.Thread
	}
	return it.Link
}

// A done request is removed after this so the inbox only holds what is still useful
const keepDone = 7 * 24 * time.Hour

// How many requests one delegation may queue
// Zero values set no limit
type Limits struct {
	PerHour int
	// A request with the same digest within this long is a duplicate
	Dedupe time.Duration
}

var (
	ErrLimited   = errors.New("the delegation queued its most requests this hour")
	ErrDuplicate = errors.New("the same request was queued moments ago")
)

func (s Store) Add(it Item, now time.Time) (Item, bool, error) {
	return s.AddLimited(it, Limits{}, now)
}

// Returns false with the stored item when the thread was seen before
//  1. A message newer than the stored one moves the author and the summary to it
//     The link stays the first one so a relay opened with it still finds the request
//  2. A done, held or question request asked again opens again and returns true
//  3. A taken request asked again opens again once its take settles
//  4. A new thread past the limits of its delegation fails with ErrLimited or ErrDuplicate counted under the lock
func (s Store) AddLimited(it Item, lim Limits, now time.Time) (Item, bool, error) {
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
	it.Id, it.Status = IdOf(it.key()), StatusOpen
	it.AddedAt, it.UpdatedAt = now.UTC(), now.UTC()
	seen, err := s.Get(it.Id)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Item{}, false, err
	case !newer(it.Ts, seen.Ts):
		return seen, false, nil
	default:
		seen.Ts, seen.From, seen.Author, seen.Summary = it.Ts, it.From, it.Author, it.Summary
		seen.Followup, seen.Correction = it.Followup, it.Correction
		switch seen.Status {
		case StatusOpen:
			return seen, false, s.put(seen)
		case StatusTaken:
			seen.Again = true
			return seen, false, s.put(seen)
		case StatusDone:
			// A follow-up of an answered request is a new round of the same request
			seen.Acked = nil
		}
		seen.Status, seen.Reason, seen.HeldUntil, seen.UpdatedAt = StatusOpen, it.Reason, time.Time{}, now.UTC()
		return seen, true, s.put(seen)
	}
	if err := s.limit(it, lim, now); err != nil {
		return Item{}, false, err
	}
	return it, true, s.put(it)
}

// Counts the requests the delegation of it queued lately
// Callers hold the lock
func (s Store) limit(it Item, lim Limits, now time.Time) error {
	if lim.PerHour <= 0 && (lim.Dedupe <= 0 || it.Digest == "") {
		return nil
	}
	all, err := s.List()
	if err != nil {
		return err
	}
	hour := 0
	for _, x := range all {
		if x.Delegation != it.Delegation || x.Id == it.Id {
			continue
		}
		age := now.Sub(x.AddedAt)
		if lim.Dedupe > 0 && it.Digest != "" && x.Digest == it.Digest && age < lim.Dedupe {
			return ErrDuplicate
		}
		if age < time.Hour {
			hour++
		}
	}
	if lim.PerHour > 0 && hour >= lim.PerHour {
		return ErrLimited
	}
	return nil
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
	if slices.Contains(legacyOpen, it.Status) {
		it.Status = StatusOpen
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
// 1. A take kept past takeFor shows as open since its session no longer works on it
// 2. A question the requester left unanswered shows as open
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
		switch {
		case it.Status == StatusTaken:
			it.Status, it.SessionId, it.Reason = StatusOpen, "", Released
		case it.Unanswered(now):
			it.Status, it.Reason = StatusOpen, Unanswered
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

// A take still unsettled after this opens again
// A session that ended gives its takes back at once so this only catches one that crashed
const takeFor = 24 * time.Hour

// Requests nobody took are closed after these
const (
	expireOpen = 14 * 24 * time.Hour
	expireHeld = 30 * 24 * time.Hour
)

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

// Puts a request off until the user takes it or until it is due
// A zero until waits for the user
func (s Store) Hold(id, sessionId string, until, now time.Time) (Item, error) {
	return s.update(id, sessionId, now, func(it *Item) error {
		if it.Status == StatusDone {
			return fmt.Errorf("request %s is %s", id, it.Status)
		}
		// The user looks at the thread when the request comes back so a newer message needs no round of its own
		it.Status, it.SessionId, it.HeldUntil, it.Again = StatusHeld, "", until.UTC(), false
		if until.IsZero() {
			it.HeldUntil = time.Time{}
		}
		return nil
	})
}

// Waits for the requester to answer a question the session asked back
func (s Store) Question(id, sessionId string, now time.Time) (Item, error) {
	return s.release(id, sessionId, StatusQuestion, now, StatusTaken)
}

// Records a reaction added to the request link so it is added once
func (s Store) Ack(id, react string) (Item, error) {
	if !slices.Contains([]string{AckSeen, AckDone}, react) {
		return Item{}, fmt.Errorf("react must be %s or %s, not %q", AckSeen, AckDone, react)
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()
	it, err := s.Get(id)
	if err != nil || slices.Contains(it.Acked, react) {
		return it, err
	}
	// Not a change of status so UpdatedAt stays
	it.Acked = append(it.Acked, react)
	return it, s.put(it)
}

func (s Store) Done(id, sessionId string, now time.Time) (Item, error) {
	return s.release(id, sessionId, StatusDone, now, StatusOpen, StatusHeld, StatusTaken, StatusQuestion, StatusDone)
}

// Gives back every take of a session that ended so the requests open again at once
func (s Store) Release(sessionId string, now time.Time) (int, error) {
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
		if it.Status != StatusTaken || it.SessionId != sessionId {
			continue
		}
		it.Status, it.SessionId, it.Reason, it.Again, it.UpdatedAt = StatusOpen, "", Released, false, now.UTC()
		if err := s.put(it); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Frees the request from the session
// 1. A request another session still works on stays with it so a session never settles a take it lost
// 2. A newer message that came during the take opens it again instead of done or a question since it may answer either
func (s Store) release(id, sessionId string, to Status, now time.Time, from ...Status) (Item, error) {
	return s.update(id, sessionId, now, func(it *Item) error {
		if !slices.Contains(from, it.Status) {
			return fmt.Errorf("request %s is %s", id, it.Status)
		}
		it.Status, it.SessionId = to, ""
		if it.Again {
			it.Status, it.Reason, it.Acked = StatusOpen, Replied, nil
		}
		it.Again = false
		return nil
	})
}

// Changes a request under the lock unless another session still works on it
func (s Store) update(id, sessionId string, now time.Time, change func(it *Item) error) (Item, error) {
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()
	it, err := s.Get(id)
	if err != nil {
		return Item{}, err
	}
	if it.takenByOther(sessionId, now) {
		return Item{}, fmt.Errorf("%w %s", ErrTaken, id)
	}
	if err := change(&it); err != nil {
		return Item{}, err
	}
	it.UpdatedAt = now.UTC()
	return it, s.put(it)
}

// Only one session takes a request since the check and the write happen under the lock
// 1. Taking again from the same session returns the item
// 2. Any request not done may be taken since the user names it
func (s Store) Take(id, sessionId string, now time.Time) (Item, error) {
	if s.Paused() {
		return Item{}, ErrPaused
	}
	unlock, err := s.lock()
	if err != nil {
		return Item{}, err
	}
	defer unlock()
	it, err := s.Get(id)
	if err != nil {
		return Item{}, err
	}
	switch {
	case it.takenByOther(sessionId, now):
		return Item{}, fmt.Errorf("%w %s", ErrTaken, id)
	case it.Status == StatusDone:
		return Item{}, fmt.Errorf("request %s is %s", id, it.Status)
	}
	it.Status, it.SessionId, it.UpdatedAt, it.HeldUntil = StatusTaken, sessionId, now.UTC(), time.Time{}
	return it, s.put(it)
}

// Another session holds a take that still keeps it
func (it Item) takenByOther(sessionId string, now time.Time) bool {
	return it.Status == StatusTaken && it.SessionId != sessionId && !it.Waiting(now)
}

// Does nothing for a request that never came through the inbox
func (s Store) DoneById(id, sessionId string, now time.Time) error {
	_, err := s.Done(id, sessionId, now)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// The request whose link or thread is the given origin
// A relay names the link it was opened with so it may be any message of the thread
func (s Store) ByOrigin(origin string) (Item, error) {
	all, err := s.List()
	if err != nil {
		return Item{}, err
	}
	for _, it := range all {
		if it.Link == origin {
			return it, nil
		}
	}
	return s.Get(IdOf(origin))
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

// Closes requests left waiting so long that answering them would no longer help
// 1. open and question past expireOpen
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
		case (it.Status == StatusOpen || it.Status == StatusQuestion) && age > expireOpen:
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
