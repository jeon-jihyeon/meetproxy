// Package delegation says which messages of any source become requests and what a session does with them once the user takes one
package delegation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

const (
	WhenMention       = "mention"        // messages that mention the user in any source
	WhenReviewRequest = "review-request" // pull requests that ask the user for a review
	WhenChannel       = "channel"        // every message of one Slack channel
	WhenDM            = "dm"             // direct messages to the user that do not mention them
	WhenOwnPR         = "own-pr"         // comments of others on the user's own pull requests

	DoAnswer      = "answer"      // relay skill
	DoReview      = "review"      // review skill on the pull request the message links
	DoInvestigate = "investigate" // investigate skill on an alert

	ApproveNever = "never" // review comments only
	ApproveSelf  = "self"  // approve only when the user wrote the request
	ApproveAny   = "any"   // approve whoever asked

	LinkPR = "github-pr" // the message must link a GitHub pull request
)

type Delegation struct {
	Id      string `json:"id"`
	When    string `json:"when"`
	Channel string `json:"channel,omitempty"`
	// Workspace host used to link a channel message such as acme.slack.com
	Host string `json:"host,omitempty"`
	// Author ids or exact display names any of which must match
	From []string `json:"from,omitempty"`
	// Words that must all appear
	Words   []string `json:"words,omitempty"`
	Link    string   `json:"link,omitempty"`
	Do      string   `json:"do"`
	Approve string   `json:"approve,omitempty"`
	// The sentence the user said
	Note string `json:"note,omitempty"`
	// Requests queued per hour at most
	// Zero is the default of the kind
	MaxPerHour int `json:"max_per_hour,omitempty"`
	// Minutes within which the same text is a duplicate
	// Zero is the default of the kind
	DedupeMinutes int `json:"dedupe_minutes,omitempty"`
}

// Defaults of channel delegations whose every message is a candidate
// Mentions and the other kinds name the user so they are rare enough to need no limit
const (
	channelPerHour = 20
	channelDedupe  = 30
)

// Requests per hour and duplicate window in minutes, zero for no limit
func (d Delegation) Limits() (perHour, dedupeMinutes int) {
	perHour, dedupeMinutes = d.MaxPerHour, d.DedupeMinutes
	if d.When != WhenChannel {
		return perHour, dedupeMinutes
	}
	if perHour == 0 {
		perHour = channelPerHour
	}
	if dedupeMinutes == 0 {
		dedupeMinutes = channelDedupe
	}
	return perHour, dedupeMinutes
}

var (
	// A mention with a pull request link is a review with no setup
	// Approval stays with the user's own requests
	DefaultReview        = Delegation{Id: "default-review", When: WhenMention, Link: LinkPR, Do: DoReview, Approve: ApproveSelf}
	DefaultReviewRequest = Delegation{Id: "default-review-request", When: WhenReviewRequest, Do: DoReview, Approve: ApproveSelf}
	// Direct messages are kept after triage
	DefaultDM    = Delegation{Id: "default-dm", When: WhenDM, Do: DoAnswer}
	DefaultOwnPR = Delegation{Id: "default-own-pr", When: WhenOwnPR, Do: DoAnswer}
	// Mentions that no delegation catches are kept after triage
	Default = Delegation{Id: "default", When: WhenMention, Do: DoAnswer}
)

// Built in after the user's delegations so one of the user goes first
var builtIn = []Delegation{DefaultReview, DefaultReviewRequest, DefaultDM, DefaultOwnPR, Default}

// Kinds whose messages name the user and match delegations of that kind
var kinds = []string{WhenMention, WhenReviewRequest, WhenDM, WhenOwnPR}

// Triage only sorts out answers to messages addressed to the user since every other delegation already names its task
func (d Delegation) Triaged() bool {
	return slices.Contains([]string{WhenMention, WhenDM, WhenOwnPR}, d.When) && d.Do == DoAnswer
}

const (
	DepthQuick = "quick" // one paragraph and one piece of evidence
	DepthDeep  = "deep"  // up to four pieces of evidence
)

// quick unless the request asks for [deep]
func Depth(text string) string {
	if strings.Contains(strings.ToLower(text), "[deep]") {
		return DepthDeep
	}
	return DepthQuick
}

var (
	digestLinks = regexp.MustCompile(`<[^>]*>|https?://\S+`)
	digestTimes = regexp.MustCompile(`\d{1,2}:\d{2}(:\d{2})?`)
	digestNoise = regexp.MustCompile(`\d+`)
	digestSpace = regexp.MustCompile(`\s+`)
)

// What repeats of one message share
// Links, times and digits differ between repeats of one alert so they are dropped before hashing
func Digest(text string) string {
	t := strings.ToLower(text)
	t = digestLinks.ReplaceAllString(t, " ")
	t = digestTimes.ReplaceAllString(t, " ")
	t = digestNoise.ReplaceAllString(t, " ")
	t = strings.TrimSpace(digestSpace.ReplaceAllString(t, " "))
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])[:12]
}

var (
	validId    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	validSkill = regexp.MustCompile(`^[\w-]+(:[\w-]+)?$`)
	channelId  = regexp.MustCompile(`^[CGD][A-Z0-9]+$`)
)

func (d Delegation) Validate() error {
	switch {
	case !validId.MatchString(d.Id) || strings.HasPrefix(d.Id, "default"):
		return fmt.Errorf("id must be lowercase letters, digits and dashes and not start with default %q", d.Id)
	case !slices.Contains(append([]string{WhenChannel}, kinds...), d.When):
		return fmt.Errorf("when must be mention, review-request, dm, own-pr or channel %q", d.When)
	case d.When == WhenChannel && (!channelId.MatchString(d.Channel) || d.Host == ""):
		return errors.New("a channel delegation needs a channel id such as C0123 and a host such as acme.slack.com")
	case !validSkill.MatchString(d.Do):
		return fmt.Errorf("do must be answer, review, investigate or a skill name %q", d.Do)
	case d.Approve != "" && !slices.Contains([]string{ApproveNever, ApproveSelf, ApproveAny}, d.Approve):
		return fmt.Errorf("approve must be never, self or any %q", d.Approve)
	case d.Link != "" && d.Link != LinkPR:
		return fmt.Errorf("link must be github-pr %q", d.Link)
	case d.MaxPerHour < 0 || d.DedupeMinutes < 0:
		return errors.New("max_per_hour and dedupe_minutes must not be negative")
	}
	return nil
}

// Reviews approve only for the user's own requests unless the delegation says otherwise
func (d Delegation) MayApprove(fromUser bool) bool {
	if d.Do != DoReview {
		return false
	}
	switch d.Approve {
	case ApproveAny:
		return true
	case ApproveNever:
		return false
	default:
		return fromUser
	}
}

// A message from any source as delegations read it
type Message struct {
	Link string `json:"link"`
	Text string `json:"text"`
	// Author id
	From string `json:"from"`
	// Author display name
	Author string `json:"author"`
}

var prLink = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)

// The first pull request link of the text, empty when there is none
func PullRequest(text string) string { return prLink.FindString(text) }

// The pull request a review works on
// 1. A review request is the pull request itself
// 2. A mention names it in its text
// 3. Empty for any task other than a review
func (d Delegation) Target(m Message) string {
	switch {
	case d.Do != DoReview:
		return ""
	case d.When == WhenReviewRequest:
		return PullRequest(m.Link)
	default:
		return PullRequest(m.Text)
	}
}

// Every filter the delegation sets must pass
// Words and authors match without case
// An author matches only as a whole since a display name is anyone's to change
func (d Delegation) Matches(m Message) bool {
	// A comment on a pull request links to it without asking for a review so only the text counts
	if d.Link == LinkPR {
		if PullRequest(m.Text) == "" {
			return false
		}
	}
	text := strings.ToLower(m.Text)
	for _, w := range d.Words {
		if !strings.Contains(text, strings.ToLower(w)) {
			return false
		}
	}
	if len(d.From) == 0 {
		return true
	}
	return slices.ContainsFunc(d.From, func(f string) bool {
		return f != "" && (strings.EqualFold(f, m.From) || strings.EqualFold(f, m.Author))
	})
}

// The delegation that took a request before
// A follow-up goes to it whatever its filters say since the thread is already its
func ById(ds []Delegation, id string) (Delegation, bool) {
	if id == "" {
		return Default, true
	}
	i := slices.IndexFunc(ds, func(d Delegation) bool { return d.Id == id })
	if i < 0 {
		return Delegation{}, false
	}
	return ds[i], true
}

// The delegation that takes the message
// 1. mention, review-request, dm and own-pr pick the first delegation of that kind the message matches
// 2. Any other key is the id of a channel delegation that must match the message
func Pick(ds []Delegation, key string, m Message) (Delegation, bool) {
	byKind := slices.Contains(kinds, key)
	for _, d := range ds {
		picked := d.When == key
		if !byKind {
			picked = d.When == WhenChannel && d.Id == key
		}
		if picked && d.Matches(m) {
			return d, true
		}
	}
	return Delegation{}, false
}

type Store struct{ file string }

func New(dataDir string) Store { return Store{file: filepath.Join(dataDir, "delegations.json")} }

// In order with the built in delegations last so the first match wins
func (s Store) List() ([]Delegation, error) {
	ds, err := s.load()
	return append(ds, builtIn...), err
}

// Replaces a delegation with the same id
func (s Store) Put(d Delegation) error {
	d.Id = strings.ToLower(strings.TrimSpace(d.Id))
	if err := d.Validate(); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	ds, err := s.load()
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(ds, func(x Delegation) bool { return x.Id == d.Id }); i >= 0 {
		ds[i] = d
	} else {
		ds = append(ds, d)
	}
	return fileio.WriteJSON(s.file, ds)
}

func (s Store) Remove(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	ds, err := s.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ds, func(x Delegation) bool { return x.Id == id })
	if i < 0 {
		return fmt.Errorf("no delegation %q", id)
	}
	return fileio.WriteJSON(s.file, slices.Delete(ds, i, i+1))
}

// Serializes every read then write across the sessions that share the delegations
func (s Store) lock() (unlock func(), err error) { return fileio.Lock(s.file + ".lock") }

func (s Store) load() ([]Delegation, error) {
	var ds []Delegation
	found, err := fileio.ReadJSON(s.file, &ds)
	if err != nil && found {
		return nil, fmt.Errorf("%s is corrupt: %w", s.file, err)
	}
	return ds, err
}
