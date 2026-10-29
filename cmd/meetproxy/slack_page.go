package main

import (
	"fmt"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
)

func (c cli) slackConversations() error {
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	ids, err := client.DirectConversations()
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]any{"host": a.Host, "channels": ids})
}

func (c cli) slackPage(kind string, f flags) error {
	if f.after == "" || f.before == "" {
		return fmt.Errorf("%w: page needs --after and --before", errUsage)
	}
	client, a, err := c.slackSession()
	if err != nil {
		return err
	}
	if kind == "parents" {
		return c.slackParents(client, f)
	}
	var found []slackapi.Message
	var next string
	switch kind {
	case "mentions":
		found, next, err = client.MentionPage(a.User, f.after, f.before, f.cursor)
	case "channel":
		found, next, err = client.HistoryPage(f.channel, f.after, f.before, f.cursor)
	case "replies":
		found, next, err = client.RepliesPage(f.channel, f.thread, f.after, f.before, f.cursor)
	default:
		return fmt.Errorf("%w: unknown page kind", errUsage)
	}
	if err != nil {
		return err
	}
	users := c.users(client)
	out := []slackMessage{}
	parents := []string{}
	if kind == "channel" {
		parents = parentTimestamps(found)
	}
	for _, m := range found {
		if !tsLess(f.after, m.Ts) || tsLess(f.before, m.Ts) {
			continue
		}
		if kind == "mentions" {
			if m.User == "" {
				continue
			}
			out = append(out, slackMessage{Author: authorOf(m, users), Channel: m.Channel.Id, From: m.User, Ts: m.Ts, Link: m.Permalink, Text: m.Text})
		} else {
			thread := cmpOr(m.ThreadTs, f.thread)
			out = append(out, slackMessage{Author: authorOf(m, users), Channel: f.channel, From: cmpOr(m.User, m.BotId), Ts: m.Ts, Link: permalink(a.Host, f.channel, m.Ts, thread), Text: m.Text, Bot: m.BotId != ""})
		}
	}
	users.save()
	return writeJSON(c.out, map[string]any{"messages": out, "cursor": next, "parents": parents})
}

func (c cli) slackParents(client slackapi.Client, f flags) error {
	found, next, limited, err := client.ParentPage(f.channel, f.after, f.before, f.cursor)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]any{"parents": parentTimestamps(found), "cursor": next, "limited": limited})
}

func parentTimestamps(messages []slackapi.Message) []string {
	parents := []string{}
	for _, m := range messages {
		if m.Subtype != "channel_join" && m.Subtype != "channel_leave" {
			parents = append(parents, cmpOr(m.ThreadTs, m.Ts))
		}
	}
	return parents
}
