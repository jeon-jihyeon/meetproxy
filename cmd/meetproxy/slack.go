package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/jeon-jihyeon/meetproxy/internal/posts"
	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
)

// Points the Slack client at a fake server in tests
// slackapi refuses any host but a loopback one so it never moves the token elsewhere
const slackAPIEnv = "MEETPROXY_SLACK_API"

// User scopes every Slack command needs
var slackScopes = []string{
	"search:read", "channels:history", "groups:history", "im:history", "mpim:history", "users:read", "chat:write",
	"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read",
}

// The line every post of meetproxy ends with
const mark = "_Written by Claude on behalf of the user_"

const (
	// A token is a few dozen characters so more on stdin is not a token
	maxTokenBytes = 4096
	// How long a looked up Slack user is trusted as it was
	userCacheFor = 24 * time.Hour
	// Replies read to tell whether the user already answered
	repliesRead = 200
)

var errNoToken = errors.New("no Slack token, run /meetproxy:slack to set one up")

// Answers the user can give to the Slack token question
var setupAnswers = []string{"now", "later", "keep"}

// A message in the shape the plugin mod reads from Slack either way
type slackMessage struct {
	Author  string `json:"author"`
	Channel string `json:"channel"`
	From    string `json:"from"`
	Ts      string `json:"ts"`
	Link    string `json:"link"`
	Text    string `json:"text"`
}

// What auth.test said of the token when it was stored or checked
type slackAuth struct {
	slackapi.Auth
	At time.Time `json:"at"`
}

type slackSetup struct {
	Answer string    `json:"answer"`
	At     time.Time `json:"at"`
}

type cachedUser struct {
	slackapi.User
	At time.Time `json:"at"`
}

func slackDir(data string) string  { return filepath.Join(data, "slack") }
func tokenFile(data string) string { return filepath.Join(slackDir(data), "token") }
func authFile(data string) string  { return filepath.Join(slackDir(data), "auth.json") }
func usersFile(data string) string { return filepath.Join(slackDir(data), "users.json") }
func setupFile(data string) string { return filepath.Join(slackDir(data), "setup.json") }

func hasToken(data string) bool {
	_, err := os.Stat(tokenFile(data))
	return err == nil
}

func newSlack(token string) (slackapi.Client, error) {
	return slackapi.New(os.Getenv(slackAPIEnv), token)
}

// The client of the stored token
func (c cli) slack() (slackapi.Client, error) {
	b, err := os.ReadFile(tokenFile(c.data))
	if errors.Is(err, os.ErrNotExist) {
		return slackapi.Client{}, errNoToken
	}
	if err != nil {
		return slackapi.Client{}, err
	}
	return newSlack(strings.TrimSpace(string(b)))
}

// Stores the token on stdin once Slack accepts it
// The token is read from stdin so it never shows in a process list or a transcript
func (c cli) slackToken() error {
	b, err := io.ReadAll(io.LimitReader(c.in, maxTokenBytes+1))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(b))
	if len(b) > maxTokenBytes || !strings.HasPrefix(token, "xoxp-") || strings.ContainsAny(token, " \t\n") {
		return fmt.Errorf("%w: stdin must hold one user token that starts with xoxp-", errUsage)
	}
	client, err := newSlack(token)
	if err != nil {
		return err
	}
	a, err := client.AuthTest()
	if err != nil {
		return err
	}
	if err := fileio.WriteAtomic(tokenFile(c.data), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if err := fileio.WriteJSON(authFile(c.data), slackAuth{a, c.now.UTC()}); err != nil {
		return err
	}
	return writeJSON(c.out, map[string]any{"user": a.User, "team": a.Team, "host": a.Host, "missing": missingScopes(a.Scopes)})
}

// Checks the token with Slack and keeps what it says
func (c cli) slackWhoami() error {
	a, err := c.slackAuth(true)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"user": a.User, "team": a.Team, "host": a.Host})
}

// What the token acts for, asked of Slack when live or when nothing is kept
func (c cli) slackAuth(live bool) (slackapi.Auth, error) {
	var kept slackAuth
	if !live {
		if found, err := fileio.ReadJSON(authFile(c.data), &kept); found && err == nil && kept.User != "" {
			return kept.Auth, nil
		}
	}
	client, err := c.slack()
	if err != nil {
		return slackapi.Auth{}, err
	}
	a, err := client.AuthTest()
	if err != nil {
		return slackapi.Auth{}, err
	}
	return a, fileio.WriteJSON(authFile(c.data), slackAuth{a, c.now.UTC()})
}

func (c cli) slackMentions(after string) error {
	if after == "" {
		return fmt.Errorf("%w: slack mentions needs --after", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	found, err := client.SearchMentions(a.User, after)
	if err != nil {
		return err
	}
	users := c.users(client)
	out := make([]slackMessage, 0, len(found))
	for _, m := range found {
		out = append(out, slackMessage{Author: users.name(m.User), Channel: m.Channel.Id, From: m.User, Ts: m.Ts, Link: m.Permalink, Text: m.Text})
	}
	return c.encodeMessages(out, users)
}

// Every message of the channel after --oldest oldest first
// A channel with more than the page bound since then reads its newest messages and says so on stderr
func (c cli) slackHistory(channel, oldest string) error {
	if oldest == "" {
		return fmt.Errorf("%w: slack history needs --oldest", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	found, truncated, err := client.History(channel, oldest)
	if err != nil {
		return err
	}
	if truncated {
		fmt.Fprintf(os.Stderr, "#%s had more than %d messages since the last check and the older ones are skipped\n", channel, slackapi.MaxPages*100)
	}
	users := c.users(client)
	out := make([]slackMessage, 0, len(found))
	for _, m := range found {
		out = append(out, slackMessage{
			Author: authorOf(m, users), Channel: channel, From: cmpOr(m.User, m.BotId), Ts: m.Ts,
			Link: "https://" + a.Host + "/archives/" + channel + "/p" + strings.Replace(m.Ts, ".", "", 1), Text: m.Text,
		})
	}
	return c.encodeMessages(out, users)
}

// Bots name themselves while people are named by their profile
func authorOf(m slackapi.Message, users *userCache) string {
	switch {
	case m.Username != "":
		return m.Username
	case m.BotProfile.Name != "":
		return m.BotProfile.Name
	default:
		return users.name(m.User)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (c cli) encodeMessages(out []slackMessage, users *userCache) error {
	sort.SliceStable(out, func(i, j int) bool { return tsLess(out[i].Ts, out[j].Ts) })
	users.save()
	return writeJSON(c.out, out)
}

func tsLess(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x < y
}

// Whether the thread of the link already has an answer after --ts
// 1. A reply of the user by hand or through meetproxy
// 2. A reply of anyone that ends with the meetproxy mark such as one a teammate's meetproxy posted
func (c cli) slackCovered(link, ts string) error {
	at, err := parseSlackLink(link)
	if err != nil {
		return err
	}
	if ts == "" {
		ts = at.ts
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	replies, err := client.Replies(at.channel, at.thread, ts, repliesRead)
	if err != nil {
		return err
	}
	covered := slices.ContainsFunc(replies, func(m slackapi.Message) bool {
		return tsLess(ts, m.Ts) && (m.User == a.User || strings.HasSuffix(strings.TrimSpace(m.Text), mark))
	})
	return writeJSON(c.out, map[string]bool{"covered": covered})
}

// Replies in the thread of the link after --after that others wrote and meetproxy did not post
// Each links to itself within the thread
func (c cli) slackReplies(link, after string) error {
	at, err := parseSlackLink(link)
	if err != nil {
		return err
	}
	if after == "" {
		return fmt.Errorf("%w: slack replies needs --after", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	replies, err := client.Replies(at.channel, at.thread, after, repliesRead)
	if err != nil {
		return err
	}
	users := c.users(client)
	out := []slackMessage{}
	for _, m := range replies {
		if !tsLess(after, m.Ts) || m.User == "" || m.User == a.User || strings.HasSuffix(strings.TrimSpace(m.Text), mark) {
			continue
		}
		out = append(out, slackMessage{
			Author: users.name(m.User), Channel: at.channel, From: m.User, Ts: m.Ts, Text: m.Text,
			Link: permalink(a.Host, at.channel, m.Ts, at.thread),
		})
	}
	return c.encodeMessages(out, users)
}

// A message link, within its thread when thread names another message
func permalink(host, channel, ts, thread string) string {
	link := "https://" + host + "/archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
	if thread != "" && thread != ts {
		link += "?thread_ts=" + thread + "&cid=" + channel
	}
	return link
}

// Direct messages to the user after --after
// 1. The user's own messages and bots are left out
// 2. One that mentions the user is left to the mention reader so it is not queued twice
func (c cli) slackDMs(after string) error {
	if after == "" {
		return fmt.Errorf("%w: slack dms needs --after", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	found, err := client.DirectMessages(after)
	if err != nil {
		return err
	}
	users := c.users(client)
	out := []slackMessage{}
	for _, m := range found {
		if !tsLess(after, m.Ts) || m.User == "" || m.User == a.User || m.BotId != "" || strings.Contains(m.Text, "<@"+a.User+">") {
			continue
		}
		out = append(out, slackMessage{
			Author: users.name(m.User), Channel: m.Channel.Id, From: m.User, Ts: m.Ts, Text: m.Text,
			Link: permalink(a.Host, m.Channel.Id, m.Ts, ""),
		})
	}
	return c.encodeMessages(out, users)
}

func (c cli) slackShared(channel string) error {
	client, err := c.slack()
	if err != nil {
		return err
	}
	shared, err := client.Shared(channel)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]bool{"shared": shared})
}

var validReaction = regexp.MustCompile(`^[a-z0-9_+-]{1,64}$`)

func (c cli) slackReact(link, name string) error {
	at, err := parseSlackLink(link)
	if err != nil {
		return err
	}
	if !validReaction.MatchString(name) {
		return fmt.Errorf("%w: --react must name an emoji such as eyes, not %q", errUsage, name)
	}
	client, err := c.slack()
	if err != nil {
		return err
	}
	return client.React(at.channel, at.ts, name)
}

// Replaces with the text on stdin or deletes a reply meetproxy posted
// Only a reply the ledger holds may change so a request can never make meetproxy edit another message
func (c cli) slackEdit(reply string, replace bool) error {
	at, err := parseSlackLink(reply)
	if err != nil {
		return err
	}
	if _, err := posts.New(c.data).Find(reply); err != nil {
		return err
	}
	client, err := c.slack()
	if err != nil {
		return err
	}
	if !replace {
		return client.Delete(at.channel, at.ts)
	}
	b, err := io.ReadAll(c.in)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return fmt.Errorf("%w: slack update needs the text on stdin", errUsage)
	}
	return client.Update(at.channel, at.ts, text)
}

// The text of the thread a message link points at, one line per message
// Read for triage and routing only so it is never stored
func (c cli) slackRead(link string, limit int) error {
	at, err := parseSlackLink(link)
	if err != nil {
		return err
	}
	client, err := c.slack()
	if err != nil {
		return err
	}
	msgs, err := client.Replies(at.channel, at.ts, "", max(limit, 1))
	if err != nil {
		return err
	}
	users := c.users(client)
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, authorOf(m, users)+": "+m.Text)
	}
	users.save()
	return writeJSON(c.out, map[string]string{"text": strings.Join(lines, "\n")})
}

// A full member of the user's own workspace
// Guests, deactivated accounts and members of another team are never trusted
func (c cli) slackTrusted(id string) error {
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	users := c.users(client)
	u, err := users.get(id)
	if err != nil {
		return err
	}
	users.save()
	trusted := a.Team != "" && u.Team == a.Team && !u.Restricted && !u.Deleted
	return writeJSON(c.out, map[string]bool{"trusted": trusted})
}

// Posts the text on stdin in the thread of the link after the same check as can-post
func (c cli) slackPost(link string) (int, error) {
	at, err := parseSlackLink(link)
	if err != nil {
		return exitFailed, err
	}
	b, err := io.ReadAll(c.in)
	if err != nil {
		return exitFailed, err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return exitUsage, fmt.Errorf("%w: slack post needs the text on stdin", errUsage)
	}
	allowed, err := c.mayPost(link)
	if err != nil {
		return exitFailed, err
	}
	if !allowed {
		return c.verdict(false)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return exitFailed, err
	}
	ts, err := client.Post(at.channel, at.thread, text)
	if err != nil {
		return exitFailed, err
	}
	fmt.Fprintln(c.out, permalink(a.Host, at.channel, ts, at.thread))
	return 0, nil
}

func (c cli) slackSession() (slackapi.Client, slackapi.Auth, error) {
	client, err := c.slack()
	if err != nil {
		return slackapi.Client{}, slackapi.Auth{}, err
	}
	a, err := c.slackAuth(false)
	return client, a, err
}

// The app manifest that asks for the user scopes and the link that creates an app from it
func (c cli) slackManifest() error {
	manifest := map[string]any{
		"display_information": map[string]string{"name": "meetproxy", "description": "Reads mentions and replies in threads for the user through meetproxy"},
		"oauth_config":        map[string]any{"scopes": map[string][]string{"user": slices.Clone(slackScopes)}},
		"settings":            map[string]bool{"org_deploy_enabled": false, "socket_mode_enabled": false, "token_rotation_enabled": false},
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]any{
		"manifest": json.RawMessage(b),
		"url":      "https://api.slack.com/apps?new_app=1&manifest_json=" + url.QueryEscape(string(b)),
	})
}

// Keeps the user's answer to the token question so the mod asks again only when it should
func (c cli) slackSetup(answer string) error {
	if !slices.Contains(setupAnswers, answer) {
		return fmt.Errorf("%w: --answer is one of %v, not %q", errUsage, setupAnswers, answer)
	}
	if err := fileio.WriteJSON(setupFile(c.data), slackSetup{answer, c.now.UTC()}); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "recorded", answer)
	return nil
}

// Where a Slack message sits
type slackAt struct {
	channel, ts string
	// ts of the thread it sits in
	thread string
}

var (
	slackLinkRe   = regexp.MustCompile(`^https://[\w-]+\.slack\.com/archives/([A-Z0-9]+)/p(\d{10})(\d{6})`)
	slackThreadRe = regexp.MustCompile(`[?&]thread_ts=(\d+\.\d+)`)
)

func parseSlackLink(link string) (slackAt, error) {
	m := slackLinkRe.FindStringSubmatch(link)
	if m == nil {
		return slackAt{}, fmt.Errorf("%w: not a Slack message link %q", errUsage, link)
	}
	at := slackAt{channel: m[1], ts: m[2] + "." + m[3]}
	at.thread = at.ts
	if t := slackThreadRe.FindStringSubmatch(link); t != nil {
		at.thread = t[1]
	}
	return at, nil
}

// Users looked up in this run and the day before
// Losing the cache only costs lookups so it is written without a lock
type userCache struct {
	file    string
	now     time.Time
	client  slackapi.Client
	users   map[string]cachedUser
	changed bool
}

func (c cli) users(client slackapi.Client) *userCache {
	u := &userCache{file: usersFile(c.data), now: c.now, client: client, users: map[string]cachedUser{}}
	if _, err := fileio.ReadJSON(u.file, &u.users); err != nil {
		u.users = map[string]cachedUser{}
	}
	return u
}

func (u *userCache) get(id string) (slackapi.User, error) {
	if cached, ok := u.users[id]; ok && u.now.Sub(cached.At) < userCacheFor {
		return cached.User, nil
	}
	got, err := u.client.UserInfo(id)
	if err != nil {
		return slackapi.User{}, err
	}
	u.users[id], u.changed = cachedUser{got, u.now.UTC()}, true
	return got, nil
}

// The display name of a user or the id when it cannot be looked up
func (u *userCache) name(id string) string {
	if id == "" {
		return ""
	}
	got, err := u.get(id)
	if err != nil || got.Name == "" {
		return id
	}
	return got.Name
}

func (u *userCache) save() {
	if !u.changed {
		return
	}
	for id, c := range u.users {
		if u.now.Sub(c.At) >= userCacheFor {
			delete(u.users, id)
		}
	}
	_ = fileio.WriteJSON(u.file, u.users)
}

// What tick and status say of Slack
type slackState struct {
	Token bool        `json:"token"`
	User  string      `json:"user,omitempty"`
	Team  string      `json:"team,omitempty"`
	Host  string      `json:"host,omitempty"`
	Setup *setupState `json:"setup,omitempty"`
	// Scopes the token lacks, for status only
	Missing []string `json:"missing,omitempty"`
}

type setupState struct {
	Answer string `json:"answer"`
	// Unix seconds
	At int64 `json:"at"`
}

// Read from disk alone so a tick never waits on Slack
func readSlackState(data string, missing bool) slackState {
	st := slackState{Token: hasToken(data)}
	var a slackAuth
	if found, err := fileio.ReadJSON(authFile(data), &a); found && err == nil && st.Token {
		st.User, st.Team, st.Host = a.User, a.Team, a.Host
		if missing {
			st.Missing = missingScopes(a.Scopes)
		}
	}
	var s slackSetup
	if found, err := fileio.ReadJSON(setupFile(data), &s); found && err == nil {
		st.Setup = &setupState{s.Answer, s.At.Unix()}
	}
	return st
}

// Messages keep < and > as Slack wrote them so a mention reads as <@U1>
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func missingScopes(granted []string) []string {
	missing := []string{}
	for _, s := range slackScopes {
		if !slices.Contains(granted, s) {
			missing = append(missing, s)
		}
	}
	return missing
}
