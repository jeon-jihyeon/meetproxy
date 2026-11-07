package listen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"
	"unicode/utf8"

	model "github.com/jeon-jihyeon/meetproxy/internal/listen"
)

const acceptTimeout = 2 * time.Second

var (
	ErrConfig = errors.New("invalid listen configuration")
	ErrEvent  = model.ErrEvent
	ErrSink   = errors.New("listen sink did not confirm durable receipt")
)

type Receipt string

type Event struct {
	Source  string          `json:"source"`
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type Delivery struct {
	Event   Event   `json:"event"`
	Receipt Receipt `json:"-"`
}

type Subscription interface {
	Next(context.Context) (Delivery, error)
	Ack(context.Context, Receipt) error
	Close() error
}

type Sink interface {
	// Nil confirms a durable receipt or the identical receipt already stored
	// Durable dead letter storage belongs to the same upper receipt contract
	Accept(context.Context, Event) error
}

type Collector struct {
	subscription Subscription
	sink         Sink
}

func New(subscription Subscription, sink Sink) (*Collector, error) {
	if absent(subscription) || absent(sink) {
		return nil, ErrConfig
	}
	return &Collector{subscription: subscription, sink: sink}, nil
}

func absent(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}

func (c *Collector) Run(ctx context.Context) (err error) {
	defer func() { err = errors.Join(err, c.subscription.Close()) }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		delivery, err := c.subscription.Next(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if delivery.Receipt == "" {
			return ErrEvent
		}
		if err := c.accept(ctx, delivery.Event); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.subscription.Ack(ctx, delivery.Receipt); err != nil {
			return err
		}
	}
}

func (c *Collector) accept(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.validate(); err != nil {
		return err
	}
	event.Payload = bytes.Clone(event.Payload)
	receipt, cancel := context.WithTimeout(ctx, acceptTimeout)
	defer cancel()
	if c.sink.Accept(receipt, event) != nil {
		return ErrSink
	}
	return receipt.Err()
}

func (event Event) validate() error {
	if err := model.Validate(event.Source, event.ID, event.Kind); err != nil {
		return err
	}
	if len(event.Payload) > 1<<20 || !utf8.Valid(event.Payload) || !json.Valid(event.Payload) {
		return ErrEvent
	}
	return nil
}
