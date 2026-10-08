package main

import (
	"cmp"
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

	"github.com/jeon-jihyeon/meetproxy/internal/dest"
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
	// Key of the request the message belongs to
	// A direct conversation is one request until it is done
	Thread string `json:"thread"`
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
		out = append(out, slackMessage{
			Author: users.name(m.User), Channel: m.Channel.Id, From: m.User, Ts: m.Ts, Link: m.Permalink, Text: m.Text,
			Thread: mentionThread(m),
		})
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
			Thread: slackThread(channel, cmpOr(m.ThreadTs, m.Ts), false),
		})
	}
	return c.encodeMessages(out, users)
}

// Search names the conversation kind and the permalink the thread
func mentionThread(m slackapi.Message) string {
	thread := m.Ts
	if l, ok := dest.ParseLink(m.Permalink); ok && l.Source == dest.Slack {
		thread = l.ThreadTs
	}
	return slackThread(m.Channel.Id, thread, m.Channel.IsIm || m.Channel.IsMpim)
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
			Link: permalink(a.Host, at.channel, m.Ts, at.thread), Thread: slackThread(at.channel, at.thread, false),
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
// 3. Each conversation is read from --after or from its own last read when that is older
// The mod moves the dm cursor past each message it settles so a conversation read less often still loses none
// 4. A quiet conversation is read every quietEvery so many quiet ones cost little
// 5. A rate limit stops the rest, which are read first next time
func (c cli) slackDMs(after string) error {
	if after == "" {
		return fmt.Errorf("%w: slack dms needs --after", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	ids, err := c.directConversations(client)
	if err != nil {
		return err
	}
	read := readDMs(c.data, ids)
	found := c.readConversations(client, ids, read, after)
	if err := fileio.WriteJSON(dmsFile(c.data), read); err != nil {
		return err
	}
	users := c.users(client)
	out := []slackMessage{}
	for _, m := range found {
		if m.User == "" || m.User == a.User || m.BotId != "" || strings.Contains(m.Text, "<@"+a.User+">") {
			continue
		}
		out = append(out, slackMessage{
			Author: users.name(m.User), Channel: m.Channel.Id, From: m.User, Ts: m.Ts, Text: m.Text,
			Link: permalink(a.Host, m.Channel.Id, m.Ts, ""), Thread: slackThread(m.Channel.Id, "", true),
		})
	}
	return c.encodeMessages(out, users)
}

const (
	// How long the list of direct conversations is trusted
	// A conversation opened meanwhile is read once the list is read again
	conversationsFor = time.Hour
	// A conversation with no message for quietAfter is read every quietEvery
	quietAfter = 24 * time.Hour
	quietEvery = 10 * time.Minute
	// Read again before the last read since a message can show a little after the time it carries
	readOverlap = time.Minute
)

func dmsFile(data string) string { return filepath.Join(slackDir(data), "dms.json") }
func conversationsFile(data string) string {
	return filepath.Join(slackDir(data), "conversations.json")
}

type conversationList struct {
	Ids []string  `json:"ids"`
	At  time.Time `json:"at"`
}

// The direct conversations of the user, listed at most once per conversationsFor
func (c cli) directConversations(client slackapi.Client) ([]string, error) {
	var kept conversationList
	if found, err := fileio.ReadJSON(conversationsFile(c.data), &kept); found && err == nil && c.now.Sub(kept.At) < conversationsFor {
		return kept.Ids, nil
	}
	ids, err := client.DirectConversations()
	if err != nil {
		return nil, err
	}
	// A list that cannot be kept is only listed again next time
	_ = fileio.WriteJSON(conversationsFile(c.data), conversationList{ids, c.now.UTC()})
	return ids, nil
}

// What was read of one direct conversation
type dmRead struct {
	// Unix seconds the last read reached, zero for one never read
	Read int64 `json:"read"`
	// ts of the newest message it has
	Last string `json:"last,omitempty"`
}

// What was read of each listed conversation
// A conversation no longer listed is forgotten
func readDMs(data string, ids []string) map[string]dmRead {
	// A lost or older file only makes every conversation count as never read
	var kept map[string]dmRead
	if _, err := fileio.ReadJSON(dmsFile(data), &kept); err != nil {
		kept = nil
	}
	read := make(map[string]dmRead, len(ids))
	for _, id := range ids {
		read[id] = kept[id]
	}
	return read
}

// Whether a conversation read before has had no message for quietAfter and was read within quietEvery
func (c cli) quiet(r dmRead) bool {
	if r.Read == 0 || c.now.Sub(time.Unix(r.Read, 0)) >= quietEvery {
		return false
	}
	last, _ := strconv.ParseFloat(r.Last, 64)
	return c.now.Sub(time.Unix(int64(last), 0)) >= quietAfter
}

// The ts a conversation is read after
func (r dmRead) oldest(after string) string {
	if r.Read == 0 {
		return after
	}
	own := strconv.FormatInt(r.Read, 10)
	if tsLess(own, after) {
		return own
	}
	return after
}

// Reads the conversations read longest ago first and records what each read reached
// Only messages after the ts each conversation was read from are returned
// A conversation that fails keeps what it had and a rate limit stops the rest
func (c cli) readConversations(client slackapi.Client, ids []string, read map[string]dmRead, after string) []slackapi.Message {
	order := slices.Clone(ids)
	slices.SortStableFunc(order, func(x, y string) int { return cmp.Compare(read[x].Read, read[y].Read) })
	var out []slackapi.Message
	for _, id := range order {
		r := read[id]
		if c.quiet(r) {
			continue
		}
		oldest := r.oldest(after)
		msgs, truncated, err := client.History(id, oldest)
		var e *slackapi.Error
		switch {
		case errors.As(err, &e) && e.Code == "ratelimited":
			fmt.Fprintf(os.Stderr, "direct messages wait since Slack rate limited reading %s\n", id)
			return out
		case err != nil:
			fmt.Fprintf(os.Stderr, "direct messages of %s wait since they could not be read: %v\n", id, err)
			continue
		case truncated:
			fmt.Fprintf(os.Stderr, "%s had more than %d messages since the last check and the older ones are skipped\n", id, slackapi.MaxPages*100)
		}
		for _, m := range msgs {
			if !tsLess(oldest, m.Ts) {
				continue
			}
			m.Channel.Id = id
			out = append(out, m)
			if tsLess(r.Last, m.Ts) {
				r.Last = m.Ts
			}
		}
		r.Read = c.now.Add(-readOverlap).Unix()
		read[id] = r
	}
	return out
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

func parseSlackLink(link string) (slackAt, error) {
	l, ok := dest.ParseLink(link)
	if !ok || l.Source != dest.Slack {
		return slackAt{}, fmt.Errorf("%w: not a Slack message link %q", errUsage, link)
	}
	return slackAt{channel: l.Channel, ts: l.Ts, thread: l.ThreadTs}, nil
}

// The key the watcher files a Slack message under
func slackThread(channel, thread string, direct bool) string {
	if direct {
		return dest.SlackDirectThread(channel)
	}
	return dest.Link{Source: dest.Slack, Channel: channel, ThreadTs: thread}.Thread()
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
