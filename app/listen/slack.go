package listen

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	model "github.com/jeon-jihyeon/meetproxy/internal/listen"
	"github.com/jeon-jihyeon/meetproxy/internal/slackevent"
)

const maxCallback = 1 << 20

type callback struct {
	Type  string          `json:"type"`
	ID    string          `json:"event_id"`
	App   string          `json:"api_app_id"`
	Team  string          `json:"team_id"`
	Event json.RawMessage `json:"event"`
}

type wireMessage struct {
	Type        string   `json:"type"`
	ChannelType string   `json:"channel_type"`
	Subtype     string   `json:"subtype"`
	Channel     string   `json:"channel"`
	User        string   `json:"user"`
	Text        wireText `json:"text"`
	BotID       string   `json:"bot_id"`
	TS          string   `json:"ts"`
	Thread      string   `json:"thread_ts"`
	Hidden      bool     `json:"hidden"`
}

type wireText struct{}

func (*wireText) UnmarshalJSON(raw []byte) error {
	if len(raw) != 0 && (raw[0] == '"' || bytes.Equal(raw, []byte("null"))) {
		return nil
	}
	return ErrEvent
}

func decode(raw []byte, bot string) (Event, bool, error) {
	if len(raw) > maxCallback || !utf8.Valid(raw) {
		return Event{}, false, ErrEvent
	}
	var in callback
	if json.Unmarshal(raw, &in) != nil {
		return Event{}, false, ErrEvent
	}
	if in.Type != "event_callback" {
		return Event{}, false, nil
	}
	var message wireMessage
	if len(in.Event) == 0 || bytes.Equal(in.Event, []byte("null")) || json.Unmarshal(in.Event, &message) != nil {
		return Event{}, false, ErrEvent
	}
	facts := slackevent.Facts{
		ID: in.ID, App: in.App, Team: in.Team, Kind: message.kind(), Channel: message.Channel, User: message.User,
		TS: message.TS, Thread: message.Thread, Bot: message.BotID != "", Hidden: message.Hidden,
	}
	accepted, err := facts.Eligible(bot)
	if err != nil {
		return Event{}, false, ErrEvent
	}
	if !accepted {
		return Event{}, false, nil
	}
	source := "slack:" + in.App + ":" + in.Team
	if err := model.Validate(source, in.ID, string(facts.Kind)); err != nil {
		return Event{}, false, err
	}
	return Event{
		Source: source, ID: in.ID,
		Kind: string(facts.Kind), Payload: in.Event,
	}, true, nil
}

func (m wireMessage) kind() slackevent.Kind {
	if m.Subtype != "" && m.Subtype != "file_share" && m.Subtype != "me_message" {
		return ""
	}
	switch {
	case m.Type == "app_mention":
		return slackevent.Mention
	case m.Type == "message" && m.ChannelType == "im":
		return slackevent.Direct
	default:
		return ""
	}
}
