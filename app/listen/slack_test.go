package listen_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackMapping(t *testing.T) {
	t.Parallel()
	direct := strings.Replace(inner, `"type":"app_mention"`, `"type":"message","channel_type":"im"`, 1)
	type want struct {
		kind string
		acks int32
		err  error
	}
	tcs := []struct {
		name string
		raw  []byte
		want want
	}{
		{"mention", callback(inner), want{"mention", 1, nil}},
		{"direct", callback(direct), want{"direct_message", 1, nil}},
		{"file", callback(strings.Replace(direct, `"type":"message"`, `"type":"message","subtype":"file_share"`, 1)), want{"direct_message", 1, nil}},
		{"me message", callback(strings.Replace(direct, `"type":"message"`, `"type":"message","subtype":"me_message"`, 1)), want{"direct_message", 1, nil}},
		{"unrelated", callback(`{"type":"reaction_added"}`), want{"", 1, listen.ErrRetries}},
		{"subscription name", callback(strings.Replace(direct, `"type":"message"`, `"type":"message.im"`, 1)), want{"", 1, listen.ErrRetries}},
		{"edited", callback(strings.Replace(direct, `"type":"message"`, `"type":"message","subtype":"message_changed"`, 1)), want{"", 1, listen.ErrRetries}},
		{"self", callback(strings.Replace(inner, `"U2"`, `"U1"`, 1)), want{"", 1, listen.ErrRetries}},
		{"hidden", callback(strings.Replace(inner, `"text":"fixture"`, `"hidden":true`, 1)), want{"", 1, listen.ErrRetries}},
		{"bot", callback(strings.Replace(inner, `"text":"fixture"`, `"bot_id":"B1"`, 1)), want{"", 1, listen.ErrRetries}},
		{"null event", callback(`null`), want{"", 0, listen.ErrEvent}},
		{"absent text", callback(strings.Replace(inner, `,"text":"fixture"`, "", 1)), want{"mention", 1, nil}},
		{"null text", callback(strings.Replace(inner, `"text":"fixture"`, `"text":null`, 1)), want{"mention", 1, nil}},
		{"number text", callback(strings.Replace(inner, `"text":"fixture"`, `"text":1`, 1)), want{"", 0, listen.ErrEvent}},
		{"boolean text", callback(strings.Replace(inner, `"text":"fixture"`, `"text":true`, 1)), want{"", 0, listen.ErrEvent}},
		{"array text", callback(strings.Replace(inner, `"text":"fixture"`, `"text":[]`, 1)), want{"", 0, listen.ErrEvent}},
		{"object text", callback(strings.Replace(inner, `"text":"fixture"`, `"text":{}`, 1)), want{"", 0, listen.ErrEvent}},
		{"invalid text escape", callback(strings.Replace(inner, `"text":"fixture"`, `"text":"\q"`, 1)), want{"", 0, listen.ErrRetries}},
		{"malformed callback", []byte(`{"type":"event_callback","event":1}`), want{"", 0, listen.ErrEvent}},
		{"missing identity", []byte(strings.Replace(string(callback(inner)), `"event_id":"Ev1"`, `"event_id":""`, 1)), want{"", 0, listen.ErrEvent}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var acks atomic.Int32
			srv := callbackServer(t, tc.raw, &acks)
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			token := filepath.Join(dir, "app-token")
			require.NoError(t, os.WriteFile(token, []byte("xapp-fixture"), 0o600))
			source, err := listen.NewSocket(listen.SocketConfig{
				Directory: filepath.Join(dir, "runtime"), TokenFile: token, APIBase: srv.URL, BotUser: "U1", Failures: 1,
			})
			require.NoError(t, err)
			defer func() { assert.NoError(t, source.Close()) }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			delivery, err := source.Next(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.kind, delivery.Event.Kind)
			if tc.want.kind != "" {
				assert.Zero(t, acks.Load())
				assert.Equal(t, "slack:A1:T1", delivery.Event.Source)
				assert.Equal(t, "Ev1", delivery.Event.ID)
				assert.NotEmpty(t, delivery.Receipt)
				assert.NotEqual(t, listen.Receipt("receipt"), delivery.Receipt)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(delivery.Event.Payload, &payload))
				assert.Equal(t, "U2", payload["user"])
				assert.Equal(t, "C1", payload["channel"])
				assert.NotContains(t, payload, "token")
				assert.NotContains(t, payload, "envelope_id")
				body, marshalErr := json.Marshal(delivery)
				require.NoError(t, marshalErr)
				assert.NotContains(t, string(body), string(delivery.Receipt))
				require.NoError(t, source.Ack(ctx, delivery.Receipt))
			}
			assert.Eventually(t, func() bool { return acks.Load() == tc.want.acks }, time.Second, time.Millisecond)
		})
	}
}
