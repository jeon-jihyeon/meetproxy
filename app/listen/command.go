package listen

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/handoff"
)

type CommandConfig struct {
	Binary      string
	Arguments   []string
	Environment []string
	Timeout     time.Duration
}

type CommandSink struct{ command *handoff.Command }

func NewCommand(config CommandConfig) (*CommandSink, error) {
	command, err := handoff.New(config.Binary, config.Arguments, config.Environment, config.Timeout)
	if err != nil {
		return nil, ErrConfig
	}
	return &CommandSink{command: command}, nil
}

func (s *CommandSink) Accept(ctx context.Context, event Event) error {
	if err := event.validate(); err != nil {
		return err
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		return ErrSink
	}
	if err := s.command.Send(ctx, bytes.TrimSuffix(body.Bytes(), []byte("\n"))); err != nil {
		return ErrSink
	}
	return nil
}
