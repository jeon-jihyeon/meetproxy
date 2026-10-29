// Package slackmcp reads the answers of the Slack connector's tools
package slackmcp

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// One tool answer as MCP gives it
type Result struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// One message a connector answer renders
type Message struct {
	Author  string `json:"author"`
	From    string `json:"from"`
	Channel string `json:"channel"`
	Ts      string `json:"ts"`
	Link    string `json:"link"`
	Text    string `json:"text"`
}

var (
	ErrFormat     = errors.New("unrecognized slack answer format")
	ErrPagination = errors.New("unrecognized slack pagination format")
	ErrMore       = errors.New("slack has more messages without a continuation cursor")
	ErrHeader     = errors.New("incomplete slack message header")
	ErrPrefix     = errors.New("unrecognized slack message prefix")
)

var (
	noResults = regexp.MustCompile(`(?i)\bno (?:results|messages|matches)\b|\bnothing found\b|^\s*Channel: [^\n]*\s*$`)
	cursorRe  = regexp.MustCompile("(?i)cursor:?\\s+`([^`]+)`")
	pagedRe   = regexp.MustCompile(`(?i)next page|cursor`)

	searchDelim   = regexp.MustCompile(`(?m)^### Result [^\n]*(?:\n|$)`)
	searchHeader  = regexp.MustCompile(`^Channel: [^\n]*\nFrom: [^\n]*\nMessage_ts: [\d.]+\nPermalink: \[link\]\(\S+\)\nText: \n`)
	channelDelim  = regexp.MustCompile(`(?m)^=== Message from `)
	channelHeader = regexp.MustCompile(`^[^\n]* ===\s*\nMessage TS: [\d.]+(?:\n|$)`)
	threadDelim   = regexp.MustCompile(`(?m)^(?:=== THREAD PARENT MESSAGE ===|--- Reply \d+ of \d+ ---)\n`)
	threadHeader  = regexp.MustCompile(`^From: [^\n]*\n(?:Time: [^\n]*\n)?Message TS: [\d.]+(?:\n|$)`)
	partialHeader = regexp.MustCompile(`^(?:[^\n]*\n)?(?:From:|Time:|Message TS:|Channel:|Message_ts:|Permalink:)`)
	allowedPrefix = regexp.MustCompile(`^(?:Channel: [^\n]*|=== THREAD REPLIES \(\d+ total\) ===)?\s*$`)
	searchAuthor  = regexp.MustCompile(`(?m)^From: ([^<(\n]*)`)
	searchChannel = regexp.MustCompile(`(?m)^Channel: .*\(ID: (\w+)\)`)
	searchFrom    = regexp.MustCompile(`(?m)^From: .*\(ID: (\w+)\)`)
	searchTs      = regexp.MustCompile(`(?m)^Message_ts: ([\d.]+)`)
	searchLink    = regexp.MustCompile(`(?m)^Permalink: \[link\]\((\S+)\)`)
	messageTs     = regexp.MustCompile(`(?m)^Message TS: ([\d.]+)`)
	messageTsLine = regexp.MustCompile(`(?m)^Message TS: .*$`)
	channelAuthor = regexp.MustCompile(`^([^<(\n]*)`)
	// The last id of the header line, since a display name can hold its own (ID) at
	channelFrom    = regexp.MustCompile(`(?m)\((\w+)\) at [^()\n]* ===[ \t]*$`)
	threadAuthor   = regexp.MustCompile(`(?m)^From: ([^<(\n]*)`)
	threadFrom     = regexp.MustCompile(`(?m)^From: .*\((\w+)\)\s*$`)
	reactionLine   = regexp.MustCompile(`(?m)^Reactions: .*$`)
	reactionSplit  = regexp.MustCompile(`[,;]\s*`)
	channelTitleRe = regexp.MustCompile(`(?m)^Channel: #?(\S[^\n]*?) \((?:ID: )?(\w+)\)\s*$`)
	userIdRe       = regexp.MustCompile(`User ID: (\w+)`)
)

// The text parts of a tool answer
func Text(r Result) string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// The field of a JSON answer, or the whole text when it holds no such field
func Field(r Result, field string) string {
	text := Text(r)
	var parsed map[string]any
	if json.Unmarshal([]byte(text), &parsed) == nil {
		if v, ok := parsed[field].(string); ok {
			return v
		}
	}
	return text
}

// An answer with text but no message in it is a format this parser no longer reads
// Failing makes the source give up its lease and shows in status instead of skipping messages quietly
func Recognized(r Result, field string, messages []Message) ([]Message, error) {
	md := Field(r, field)
	if len(messages) > 0 || strings.TrimSpace(md) == "" || noResults.MatchString(md) {
		return messages, nil
	}
	return nil, ErrFormat
}

// The cursor of the next page, empty on the last one
func NextPage(r Result) (string, error) {
	var parsed map[string]any
	if json.Unmarshal([]byte(Text(r)), &parsed) != nil {
		return "", nil
	}
	info := ""
	if v, ok := parsed["pagination_info"]; ok && v != nil {
		s, isString := v.(string)
		if !isString {
			return "", ErrPagination
		}
		info = s
	}
	if m := cursorRe.FindStringSubmatch(info); m != nil {
		return m[1], nil
	}
	if pagedRe.MatchString(info) {
		return "", ErrPagination
	}
	if more, _ := parsed["has_more"].(bool); more {
		return "", ErrMore
	}
	return "", nil
}

// The search tool answers with JSON whose results field is markdown with one block per message
func SearchMessages(r Result) ([]Message, error) {
	parts, err := blocks(Field(r, "results"), searchDelim, searchHeader)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(parts))
	for _, b := range parts {
		out = append(out, Message{
			Author: strings.TrimSpace(group(searchAuthor, b)), Channel: group(searchChannel, b), From: group(searchFrom, b),
			Ts: group(searchTs, b), Link: group(searchLink, b), Text: searchBody(b),
		})
	}
	return sorted(out), nil
}

// The channel reader answers with one block per message and no permalink, so one is built from the host
func ChannelMessages(r Result, channel, host string) ([]Message, error) {
	parts, err := blocks(Field(r, "messages"), channelDelim, channelHeader)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(parts))
	for _, b := range parts {
		ts := group(messageTs, b)
		out = append(out, Message{
			Author: strings.TrimSpace(group(channelAuthor, b)), From: group(channelFrom, b), Channel: channel, Ts: ts,
			Link: Permalink(host, channel, ts), Text: messageText(b),
		})
	}
	return sorted(out), nil
}

// The detailed thread reader answers with the parent block and one block per reply
func ThreadMessages(r Result, channel, host string) ([]Message, error) {
	parts, err := blocks(Field(r, "messages"), threadDelim, threadHeader)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(parts))
	for _, b := range parts {
		ts := group(messageTs, b)
		out = append(out, Message{
			Author: strings.TrimSpace(group(threadAuthor, b)), From: group(threadFrom, b), Channel: channel, Ts: ts,
			Link: Permalink(host, channel, ts), Text: messageText(b),
		})
	}
	return sorted(out), nil
}

// The reaction names each message of a thread shows
func ReactionLines(m Message) []string {
	var out []string
	for _, line := range reactionLine.FindAllString(m.Text, -1) {
		out = append(out, reactionSplit.Split(strings.TrimPrefix(line, "Reactions: "), -1)...)
	}
	return out
}

// The channel name a channel reader answer starts with, empty when it shows none
// 1. Only the header before the first message block counts, so a body line never names the channel
// 2. The id is checked so a header for another channel never names this one
func ChannelTitle(r Result, channel string) string {
	md := Field(r, "messages")
	if loc := channelDelim.FindStringIndex(md); loc != nil {
		md = md[:loc[0]]
	}
	m := channelTitleRe.FindStringSubmatch(md)
	if m == nil || m[2] != channel {
		return ""
	}
	return m[1]
}

// The user id a profile answer names
func UserId(r Result) string { return group(userIdRe, Text(r)) }

// A message link the connector names in a send answer
var linkRe = regexp.MustCompile(`https://[\w-]+\.slack\.com/archives/[A-Z0-9]+/p\d{16}[^\s)>|]*`)

var (
	linkChannel  = regexp.MustCompile(`/archives/([A-Z0-9]+)/`)
	linkTs       = regexp.MustCompile(`/p(\d{10})(\d{6})`)
	linkThreadTs = regexp.MustCompile(`[?&]thread_ts=([\d.]+)`)
)

// The last message link of channel a send answer names that can be the reply
// 1. A link to another channel is never the reply
// 2. A message the sent body links is never the reply, since the answer may echo the body
// 3. In a thread the parent is never the reply, and a link naming another thread is not either
func SentLinkIn(r Result, channel, thread, body string) string {
	quoted := map[string]bool{}
	for _, link := range linkRe.FindAllString(body, -1) {
		quoted[linkTsOf(link)] = true
	}
	reply := ""
	for _, link := range linkRe.FindAllString(Text(r), -1) {
		ts := linkTsOf(link)
		switch {
		case group(linkChannel, link) != channel, quoted[ts]:
			continue
		case thread != "" && (ts == thread || cmpOr(group(linkThreadTs, link), thread) != thread):
			continue
		}
		reply = link
	}
	return reply
}

// The message ts of a message link
func linkTsOf(link string) string {
	m := linkTs.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func Permalink(host, channel, ts string) string {
	if ts == "" {
		return ""
	}
	return "https://" + host + "/archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
}

func group(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func sorted(out []Message) []Message {
	sort.SliceStable(out, func(i, j int) bool { return tsValue(out[i].Ts) < tsValue(out[j].Ts) })
	return out
}

func tsValue(ts string) float64 {
	f, _ := strconv.ParseFloat(ts, 64)
	return f
}

// Rendered delimiters have meaning only with their immediate metadata header
// So request text that repeats a delimiter line stays part of the body it sits in
func blocks(md string, delim, header *regexp.Regexp) ([]string, error) {
	type boundary struct{ at, start int }
	var bs []boundary
	for _, loc := range delim.FindAllStringIndex(md, -1) {
		tail := md[loc[1]:]
		switch {
		case header.MatchString(tail):
			bs = append(bs, boundary{loc[0], loc[1]})
		case partialHeader.MatchString(tail):
			return nil, ErrHeader
		}
	}
	if len(bs) > 0 && !allowedPrefix.MatchString(md[:bs[0].at]) {
		return nil, ErrPrefix
	}
	out := make([]string, 0, len(bs))
	for i, b := range bs {
		end := len(md)
		if i+1 < len(bs) {
			end = bs[i+1].at
		}
		out = append(out, md[b.start:end])
	}
	return out, nil
}

// Rendered metadata can also be request text so no matching line may cut the observed body
func messageText(block string) string {
	loc := messageTsLine.FindStringIndex(block)
	if loc == nil {
		return ""
	}
	return strings.TrimSpace(block[loc[1]:])
}

func searchBody(block string) string {
	at := strings.Index(block, "Text: \n")
	if at < 0 {
		return ""
	}
	return strings.TrimSpace(block[at+len("Text: \n"):])
}
