package main

import "github.com/jeon-jihyeon/meetproxy/internal/messageurl"

import (
	"fmt"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
)

func (c cli) snapshot(link string) error {
	l, ok := messageurl.ParseLink(link)
	if !ok {
		return fmt.Errorf("snapshot needs a supported message link")
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
	messages, err := threadMessages(client, l.Channel, l.ThreadTs)
	if err != nil {
		return err
	}
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

// Every message of a thread page by page within MaxPages
func threadMessages(client slackapi.Client, channel, thread string) ([]slackapi.Message, error) {
	var messages []slackapi.Message
	cursor, seen := "", map[string]bool{}
	for range slackapi.MaxPages {
		page, next, err := client.RepliesPage(channel, thread, "0", "", cursor)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page...)
		if next == "" {
			return messages, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("the slack thread repeated a page cursor")
		}
		seen[next], cursor = true, next
	}
	return nil, fmt.Errorf("the slack thread exceeds the 50 page limit")
}
