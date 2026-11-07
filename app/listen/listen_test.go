package listen_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/jeon-jihyeon/meetproxy/app/listen/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const inner = `{"type":"app_mention","user":"U2","channel":"C1","ts":"1893456000.000001","text":"fixture"}`

func callback(event string) []byte {
	return []byte(`{"type":"event_callback","event_id":"Ev1","api_app_id":"A1","team_id":"T1",` +
		`"token":"fixture","event":` + event + `}`)
}

func sample() listen.Event {
	return listen.Event{Source: "acme:files", ID: "change-1", Kind: "changed", Payload: json.RawMessage(`{"path":"acme"}`)}
}

func TestRun(t *testing.T) {
	t.Parallel()
	fault := errors.New("private upstream error text")
	type args struct {
		deliveries int
		sinkErr    error
		nextErr    error
		ackErr     error
		closeErr   error
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"non Slack source", args{deliveries: 1, nextErr: io.EOF}, nil},
		{"duplicate event", args{deliveries: 2, nextErr: io.EOF}, nil},
		{"sink failure", args{deliveries: 1, sinkErr: fault}, listen.ErrSink},
		{"source failure", args{nextErr: fault}, fault},
		{"ACK failure", args{deliveries: 1, ackErr: listen.ErrReceipt}, listen.ErrReceipt},
		{"close failure", args{nextErr: io.EOF, closeErr: fault}, fault},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
			event := sample()
			before := append(json.RawMessage(nil), event.Payload...)
			var order []any
			for range tc.args.deliveries {
				order = append(order, source.EXPECT().Next(gomock.Any()).Return(
					listen.Delivery{Event: event, Receipt: "opaque"}, nil))
				order = append(order, sink.EXPECT().Accept(gomock.Any(), gomock.Any()).DoAndReturn(
					func(ctx context.Context, in listen.Event) error {
						deadline, ok := ctx.Deadline()
						assert.True(t, ok)
						assert.LessOrEqual(t, time.Until(deadline), 2*time.Second)
						assert.Equal(t, event, in)
						in.Payload[0] = 'x'
						return tc.args.sinkErr
					}))
				if tc.args.sinkErr == nil {
					order = append(order, source.EXPECT().Ack(gomock.Any(), listen.Receipt("opaque")).Return(tc.args.ackErr))
				}
			}
			if tc.args.sinkErr == nil && tc.args.ackErr == nil {
				order = append(order, source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{}, tc.args.nextErr))
			}
			order = append(order, source.EXPECT().Close().Return(tc.args.closeErr))
			gomock.InOrder(order...)
			collector, err := listen.New(source, sink)
			require.NoError(t, err)

			err = collector.Run(context.Background())

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, before, event.Payload)
		})
	}
}

func TestRunInvalidEvent(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name    string
		event   listen.Event
		receipt listen.Receipt
	}{
		{"missing source", listen.Event{ID: "1", Kind: "changed", Payload: json.RawMessage(`{}`)}, "opaque"},
		{"invalid JSON", listen.Event{Source: "acme", ID: "1", Kind: "changed", Payload: json.RawMessage(`{`)}, "opaque"},
		{"invalid UTF8", listen.Event{Source: "acme", ID: "1", Kind: "changed", Payload: []byte{34, 255, 34}}, "opaque"},
		{"over byte limit", listen.Event{Source: "acme", ID: "1", Kind: "changed",
			Payload: json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)}, "opaque"},
		{"empty receipt", sample(), ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
			source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{Event: tc.event, Receipt: tc.receipt}, nil)
			source.EXPECT().Close().Return(nil)
			collector, err := listen.New(source, sink)
			require.NoError(t, err)

			assert.ErrorIs(t, collector.Run(context.Background()), listen.ErrEvent)
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		before bool
	}{
		{"before receive", true}, {"after durable receipt", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctrl := gomock.NewController(t)
			source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
			if tc.before {
				cancel()
			} else {
				source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{Event: sample(), Receipt: "opaque"}, nil)
				sink.EXPECT().Accept(gomock.Any(), gomock.Any()).DoAndReturn(
					func(context.Context, listen.Event) error { cancel(); return nil })
			}
			source.EXPECT().Close().Return(nil)
			collector, err := listen.New(source, sink)
			require.NoError(t, err)
			assert.ErrorIs(t, collector.Run(ctx), context.Canceled)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	var source *mocks.MockSubscription
	var sink *mocks.MockSink
	tcs := []struct {
		name   string
		source listen.Subscription
		sink   listen.Sink
	}{
		{"nil source", nil, mocks.NewMockSink(gomock.NewController(t))},
		{"typed nil source", source, mocks.NewMockSink(gomock.NewController(t))},
		{"nil sink", mocks.NewMockSubscription(gomock.NewController(t)), nil},
		{"typed nil sink", mocks.NewMockSubscription(gomock.NewController(t)), sink},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			collector, err := listen.New(tc.source, tc.sink)
			assert.Nil(t, collector)
			assert.ErrorIs(t, err, listen.ErrConfig)
		})
	}
}

func TestRunDeadline(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
	source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{Event: sample(), Receipt: "opaque"}, nil)
	sink.EXPECT().Accept(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ listen.Event) error { <-ctx.Done(); return nil })
	source.EXPECT().Close().Return(nil)
	collector, err := listen.New(source, sink)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	assert.ErrorIs(t, collector.Run(ctx), context.DeadlineExceeded)
}

func TestRunCancellationDuringNext(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source.EXPECT().Next(gomock.Any()).DoAndReturn(func(context.Context) (listen.Delivery, error) {
		cancel()
		return listen.Delivery{Event: sample(), Receipt: "opaque"}, nil
	})
	source.EXPECT().Close().Return(nil)
	collector, err := listen.New(source, sink)
	require.NoError(t, err)

	assert.ErrorIs(t, collector.Run(ctx), context.Canceled)
}

func TestRunPayloadBoundary(t *testing.T) {
	t.Parallel()
	type want struct {
		accepts, acks int
		err           error
	}
	tcs := []struct {
		name  string
		bytes int
		want  want
	}{
		{"at limit", 1 << 20, want{1, 1, nil}},
		{"over limit", (1 << 20) + 1, want{0, 0, listen.ErrEvent}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			source, sink := mocks.NewMockSubscription(ctrl), mocks.NewMockSink(ctrl)
			event := sample()
			event.Payload = json.RawMessage(`"` + strings.Repeat("x", tc.bytes-2) + `"`)
			source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{Event: event, Receipt: "opaque"}, nil)
			sink.EXPECT().Accept(gomock.Any(), event).Return(nil).Times(tc.want.accepts)
			source.EXPECT().Ack(gomock.Any(), listen.Receipt("opaque")).Return(nil).Times(tc.want.acks)
			if tc.want.err == nil {
				source.EXPECT().Next(gomock.Any()).Return(listen.Delivery{}, io.EOF)
			}
			source.EXPECT().Close().Return(nil)
			collector, err := listen.New(source, sink)
			require.NoError(t, err)
			assert.ErrorIs(t, collector.Run(context.Background()), tc.want.err)
		})
	}
}
