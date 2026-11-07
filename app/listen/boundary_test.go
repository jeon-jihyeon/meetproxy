package listen_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/jeon-jihyeon/meetproxy/app/listen/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCallbackBoundary(t *testing.T) {
	t.Parallel()
	limit := 1 << 20
	textSize := limit - len(callback(inner)) + len("fixture")
	type want struct {
		utf8  bool
		calls int32
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		want want
	}{
		{"at byte limit", callback(strings.Replace(inner, "fixture", strings.Repeat("x", textSize), 1)),
			want{true, 1}},
		{"over byte limit", callback(strings.Replace(inner, "fixture", strings.Repeat("x", textSize+1), 1)),
			want{true, 0}},
		{"invalid UTF8 in text", callback(strings.Replace(inner, "fixture", string([]byte{0xff}), 1)),
			want{false, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, json.Valid(tc.raw))
			require.Equal(t, tc.want.utf8, utf8.Valid(tc.raw))
			var calls, acks atomic.Int32
			server := callbackServer(t, tc.raw, &acks)
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			token := filepath.Join(dir, "token")
			require.NoError(t, os.WriteFile(token, []byte("xapp-fixture"), 0o600))
			source, err := listen.NewSocket(listen.SocketConfig{
				Directory: filepath.Join(dir, "runtime"), TokenFile: token, APIBase: server.URL, Failures: 1,
				BotUser: "U1",
			})
			require.NoError(t, err)
			sink := mocks.NewMockSink(gomock.NewController(t))
			sink.EXPECT().Accept(gomock.Any(), gomock.Any()).DoAndReturn(
				func(context.Context, listen.Event) error { calls.Add(1); return nil }).AnyTimes()
			collector, err := listen.New(source, sink)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err = collector.Run(ctx)

			if tc.want.calls == 0 {
				assert.ErrorIs(t, err, listen.ErrEvent)
			} else {
				assert.ErrorIs(t, err, listen.ErrRetries)
			}
			assert.Equal(t, tc.want.calls, calls.Load())
			assert.Equal(t, tc.want.calls, acks.Load())
		})
	}
}

func callbackServer(t *testing.T, raw []byte, acks *atomic.Int32) *httptest.Server {
	t.Helper()
	return eventServer(t, func(_ int32, _ net.Conn, rw *bufio.ReadWriter) {
		if sendCallback(rw, "receipt", raw) != nil {
			return
		}
		if id, err := readReceipt(rw); err == nil && id == "receipt" {
			acks.Add(1)
		}
	})
}
