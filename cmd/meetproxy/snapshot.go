package main

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"fmt"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
)

func (c cli) snapshot(link string) error {
	l, ok := messageurl.ParseLink(link)
	if !ok {
		return fmt.Errorf("snapshot needs a Slack or GitHub message link")
	}
	if l.Source == messageurl.GitHub {
		messages, err := newGitHub().Snapshot(l)
		if err != nil {
			return err
		}
		return writeJSON(c.out, map[string]any{"messages": messages})
	}
	client, err := c.slack()
	if err != nil {
		return err
	}
	var messages []slackapi.Message
	cursor, seen := "", map[string]bool{}
	for range slackapi.MaxPages {
		page, next, err := client.RepliesPage(l.Channel, l.ThreadTs, "0", "", cursor)
		if err != nil {
			return err
		}
		messages = append(messages, page...)
		if next == "" {
			seenMessages := map[string]bool{}
			expected := 0
			for _, m := range messages {
				seenMessages[m.Ts] = true
				if m.Ts == l.ThreadTs {
					expected = m.ReplyCount + 1
				}
			}
			if expected == 0 {
				return fmt.Errorf("thread snapshot has no parent message")
			}
			if expected > len(seenMessages) {
				return fmt.Errorf("thread snapshot is incomplete: read %d of %d messages", len(seenMessages), expected)
			}
			return writeJSON(c.out, map[string]any{"messages": messages})
		}
		if seen[next] {
			return fmt.Errorf("snapshot repeated a page cursor")
		}
		seen[next], cursor = true, next
	}
	return fmt.Errorf("snapshot exceeds the 50 page limit")
}
