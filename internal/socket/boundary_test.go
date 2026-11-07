package socket

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func largeEnvelope(size int) []byte {
	prefix := `{"type":"events_api","envelope_id":"receipt","payload":{"text":"`
	suffix := `"}}`
	return []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
}

func sendParts(p *peer, raw []byte, parts int) {
	for part := range parts {
		op := opContinue
		if part == 0 {
			op = opText
		}
		if err := p.send(part == parts-1, op, raw[len(raw)*part/parts:len(raw)*(part+1)/parts]); err != nil {
			return
		}
	}
}

func TestReadMessageSize(t *testing.T) {
	t.Parallel()
	type args struct{ size, parts int }
	type want struct {
		size int
		err  string
	}
	for _, tc := range []struct {
		name string
		args args
		want want
	}{
		{"frame at limit", args{16 << 20, 1}, want{16 << 20, "<nil>"}},
		{"frame over limit", args{16<<20 + 1, 1}, want{0, "websocket: frame of 16777217 bytes over the limit"}},
		{"message at limit", args{16 << 20, 2}, want{16 << 20, "<nil>"}},
		{"message over limit", args{16<<20 + 1, 2}, want{0, "websocket: message over the limit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := largeEnvelope(tc.args.size)
			require.True(t, json.Valid(payload))
			server := wsServer(t, "", func(p *peer) { sendParts(p, payload, tc.args.parts); _, _ = p.recv() })
			conn, err := Dial(context.Background(), wsURL(server), nil, time.Second)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			raw, err := conn.ReadMessage()

			assert.Equal(t, tc.want.err, fmt.Sprint(err))
			assert.Len(t, raw, tc.want.size)
		})
	}
}

func TestSubscriptionMessageSize(t *testing.T) {
	t.Parallel()
	type args struct{ size, parts int }
	for _, tc := range []struct {
		name string
		args args
		want int32
	}{
		{"frame at limit", args{16 << 20, 1}, 1},
		{"frame over limit", args{16<<20 + 1, 1}, 0},
		{"message at limit", args{16 << 20, 2}, 1},
		{"message over limit", args{16<<20 + 1, 2}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := largeEnvelope(tc.args.size)
			require.True(t, json.Valid(payload))
			var receipts, acks atomic.Int32
			server := newSlackServer(t, "xapp-fixture", func(_ int32, p *peer) {
				sendParts(p, payload, tc.args.parts)
				frame, err := p.recv()
				if err == nil && string(frame.data) == `{"envelope_id":"receipt"}` {
					acks.Add(1)
				}
			})
			client, err := New(server.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { assert.NoError(t, client.Close()) }()
			client.MaxFailures = 1
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			delivery, err := client.Next(ctx)
			if err == nil {
				receipts.Add(1)
				require.NoError(t, client.Ack(ctx, delivery.Receipt))
				_, err = client.Next(ctx)
			}

			assert.ErrorIs(t, err, ErrRetries)
			assert.Equal(t, tc.want, receipts.Load())
			assert.Equal(t, tc.want, acks.Load())
		})
	}
}
