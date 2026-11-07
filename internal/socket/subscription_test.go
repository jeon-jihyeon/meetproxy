package socket

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionReceipts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	noACK, ackNow := make(chan struct{}), make(chan struct{})
	frames := make(chan clientFrame, 2)
	srv := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
		require.NoError(t, p.send(true, opText, encodedEnvelope("e1", "events_api")))
		require.NoError(t, p.conn.SetReadDeadline(time.Now().Add(40*time.Millisecond)))
		_, err := p.recv()
		assert.Error(t, err)
		require.NoError(t, p.conn.SetReadDeadline(time.Time{}))
		close(noACK)
		select {
		case <-ackNow:
		case <-ctx.Done():
			return
		}
		for _, id := range []string{"e1", "e2"} {
			if id == "e2" {
				require.NoError(t, p.send(true, opText, encodedEnvelope(id, "events_api")))
			}
			frame, err := p.recv()
			if err != nil {
				return
			}
			frames <- frame
		}
		_, _ = p.recv()
	})
	client, err := New(srv.URL+"/api", "xapp-fixture")
	require.NoError(t, err)
	defer func() { assert.NoError(t, client.Close()) }()
	read, stop := context.WithCancel(ctx)
	first, err := client.Next(read)
	stop()
	require.NoError(t, err)
	assert.JSONEq(t, `{"k":"e1"}`, string(first.Payload))
	assert.NotContains(t, first.Receipt, "e1")
	select {
	case <-noACK:
	case <-ctx.Done():
		t.Fatal("a delivery was acknowledged before Ack")
	}
	_, err = client.Next(ctx)
	assert.ErrorIs(t, err, ErrPending)
	for _, receipt := range []string{"", "e1", "Ev1", "foreign"} {
		assert.ErrorIs(t, client.Ack(ctx, receipt), ErrReceipt)
	}
	close(ackNow)
	require.NoError(t, client.Ack(ctx, first.Receipt))
	require.NoError(t, client.Ack(ctx, first.Receipt))
	second, err := client.Next(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first.Receipt, second.Receipt)
	require.NoError(t, client.Ack(ctx, first.Receipt))
	require.NoError(t, client.Ack(ctx, second.Receipt))
	require.NoError(t, client.Ack(ctx, second.Receipt))
	assert.ErrorIs(t, client.Ack(ctx, first.Receipt), ErrReceipt)
	for _, id := range []string{"e1", "e2"} {
		select {
		case frame := <-frames:
			assert.Equal(t, opText, frame.op)
			assert.JSONEq(t, `{"envelope_id":"`+id+`"}`, string(frame.data))
		case <-ctx.Done():
			t.Fatal("expected explicit acknowledgement")
		}
	}
}

func TestSubscriptionConnectionGeneration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	refresh, secondConnected := make(chan struct{}), make(chan struct{})
	frames := make(chan clientFrame, 2)
	payload := json.RawMessage(`{"event_id":"Ev1","text":"acme"}`)
	srv := newSlackServer(t, "xapp-fixture", func(n int32, p *peer) {
		id := "e1"
		if n > 1 {
			id = "e2"
		}
		message, err := json.Marshal(map[string]any{
			"type": "events_api", "envelope_id": id, "payload": payload,
		})
		require.NoError(t, err)
		require.NoError(t, p.send(true, opText, message))
		if n == 1 {
			select {
			case <-refresh:
			case <-ctx.Done():
				return
			}
			require.NoError(t, p.send(true, opPing, []byte("acme")))
			frame, err := p.recv()
			require.NoError(t, err)
			frames <- frame
			require.NoError(t, p.send(true, opText, []byte(`{"type":"disconnect","reason":"refresh_requested"}`)))
			_, _ = p.recv()
			return
		}
		close(secondConnected)
		frame, err := p.recv()
		if err == nil {
			frames <- frame
		}
		_, _ = p.recv()
	})
	client, err := New(srv.URL+"/api", "xapp-fixture")
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	client.MaxFailures = 1
	first, err := client.Next(ctx)
	require.NoError(t, err)
	close(refresh)
	select {
	case <-secondConnected:
	case <-ctx.Done():
		t.Fatal("reader did not process the pending connection refresh")
	}
	assert.ErrorIs(t, client.Ack(ctx, first.Receipt), ErrReceipt)
	second, err := client.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.Payload, second.Payload)
	assert.NotEqual(t, first.Receipt, second.Receipt)
	otherServer := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
		_ = p.send(true, opText, encodedEnvelope("e2", "events_api"))
		_, _ = p.recv()
	})
	other, err := New(otherServer.URL+"/api", "xapp-fixture")
	require.NoError(t, err)
	defer func() { _ = other.Close() }()
	foreign, err := other.Next(ctx)
	require.NoError(t, err)
	assert.ErrorIs(t, client.Ack(ctx, foreign.Receipt), ErrReceipt)
	assert.ErrorIs(t, other.Ack(ctx, second.Receipt), ErrReceipt)
	require.NoError(t, client.Ack(ctx, second.Receipt))
	for _, op := range []byte{opPong, opText} {
		select {
		case frame := <-frames:
			assert.Equal(t, op, frame.op)
		case <-ctx.Done():
			t.Fatal("reader did not preserve control or ACK output")
		}
	}
}

func TestSubscriptionClose(t *testing.T) {
	t.Parallel()
	connected := make(chan struct{})
	srv := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
		close(connected)
		_, _ = p.recv()
	})
	client, err := New(srv.URL+"/api", "xapp-fixture")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Next(ctx); result <- err }()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("subscription did not connect")
	}
	_, err = client.Next(ctx)
	assert.ErrorIs(t, err, ErrPending)
	require.NoError(t, client.Close())
	require.NoError(t, client.Close())
	_, err = client.Next(ctx)
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, client.Ack(ctx, "foreign"), ErrClosed)
	select {
	case err := <-result:
		assert.ErrorIs(t, err, ErrClosed)
	case <-ctx.Done():
		t.Fatal("Close did not release Next")
	}
}

func TestSubscriptionCancellation(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name    string
		deliver bool
	}{
		{"blocked Next", false},
		{"Ack after cancellation", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			connected, disconnected := make(chan struct{}), make(chan struct{})
			var acked atomic.Bool
			srv := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
				defer close(disconnected)
				close(connected)
				if tc.deliver {
					_ = p.send(true, opText, encodedEnvelope("e1", "events_api"))
				}
				frame, err := p.recv()
				acked.Store(err == nil && frame.op == opText)
			})
			client, err := New(srv.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			life, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			ctx, cancel := context.WithCancel(life)
			defer cancel()
			go func() {
				<-connected
				if !tc.deliver {
					cancel()
				}
			}()
			delivery, err := client.Next(ctx)
			if tc.deliver {
				require.NoError(t, err)
				cancel()
				err = client.Ack(ctx, delivery.Receipt)
			}
			assert.ErrorIs(t, err, context.Canceled)
			select {
			case <-disconnected:
			case <-life.Done():
				t.Fatal("cancellation did not close the connection")
			}
			assert.False(t, acked.Load())
			_, err = client.Next(life)
			assert.ErrorIs(t, err, ErrClosed)
		})
	}
}

func TestSubscriptionBoundedQueue(t *testing.T) {
	t.Parallel()
	type args struct {
		count int
		bytes int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"count limit", args{9, 4}},
		{"byte limit", args{2, 8 << 20}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var acked atomic.Bool
			closed := make(chan struct{})
			srv := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
				defer close(closed)
				message, err := json.Marshal(map[string]any{
					"envelope_id": "e1", "type": "events_api", "payload": strings.Repeat("a", tc.args.bytes),
				})
				require.NoError(t, err)
				for range tc.args.count {
					if p.send(true, opText, message) != nil {
						return
					}
				}
				frame, err := p.recv()
				acked.Store(err == nil && frame.op == opText)
			})
			client, err := New(srv.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			client.MaxFailures = 1
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = client.Next(ctx)
			if err == nil {
				select {
				case <-closed:
				case <-ctx.Done():
					t.Fatal("queue overflow did not close the connection")
				}
				assert.Eventually(t, func() bool {
					_, err = client.Next(ctx)
					return !errors.Is(err, ErrPending)
				}, time.Second, time.Millisecond)
			}
			assert.ErrorIs(t, err, ErrRetries)
			assert.False(t, acked.Load())
		})
	}
}

func TestSubscriptionInvalidConfiguration(t *testing.T) {
	t.Parallel()
	client, err := New("", "xapp-fixture")
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	client.MaxFailures = 0
	_, err = client.Next(context.Background())
	assert.True(t, errors.Is(err, errConnection))
}

func TestSubscriptionAckCancellationDuringWrite(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name  string
		close bool
		want  error
	}{
		{"caller cancellation", false, context.Canceled},
		{"concurrent Close", true, errConnection},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			writing, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			srv := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
				message, err := json.Marshal(map[string]any{
					"envelope_id": strings.Repeat("a", 12<<20), "type": "events_api", "payload": "acme",
				})
				require.NoError(t, err)
				if p.send(true, opText, message) != nil {
					return
				}
				if _, err := p.rw.ReadByte(); err == nil {
					close(writing)
				}
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			client, err := New(srv.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			delivery, err := client.Next(ctx)
			require.NoError(t, err)
			ack, stop := context.WithCancel(ctx)
			defer stop()
			result := make(chan error, 1)
			go func() { result <- client.Ack(ack, delivery.Receipt) }()
			select {
			case <-writing:
			case <-ctx.Done():
				t.Fatal("the ACK write did not start")
			}
			started := time.Now()
			if tc.close {
				require.NoError(t, client.Close())
			} else {
				stop()
			}
			select {
			case err := <-result:
				assert.ErrorIs(t, err, tc.want)
			case <-ctx.Done():
				t.Fatal("the interrupted ACK write did not return")
			}
			assert.Less(t, time.Since(started), 2*time.Second)
			require.NoError(t, client.Close())
			_, err = client.Next(ctx)
			assert.ErrorIs(t, err, ErrClosed)
		})
	}
}
