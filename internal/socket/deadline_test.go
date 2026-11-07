package socket_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/socket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialTLSDeadline(t *testing.T) {
	t.Parallel()
	type args struct {
		dial, parent time.Duration
		cancel       bool
	}
	for _, tc := range []struct {
		name string
		args args
		want error
	}{
		{"connection budget", args{40 * time.Millisecond, 600 * time.Millisecond, false}, context.DeadlineExceeded},
		{"earlier caller deadline", args{time.Second, 40 * time.Millisecond, false}, context.DeadlineExceeded},
		{"caller cancellation", args{time.Second, time.Second, true}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			ready, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(release); _ = listener.Close() })
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if _, err = io.ReadFull(conn, make([]byte, 5)); err != nil {
					return
				}
				close(ready)
				<-release
			}()
			ctx, cancel := context.WithTimeout(context.Background(), tc.args.parent)
			defer cancel()
			result := make(chan error, 1)
			started := time.Now()
			go func() {
				conn, err := socket.Dial(ctx, "wss://"+listener.Addr().String(), nil, tc.args.dial)
				if conn != nil {
					_ = conn.Close()
				}
				result <- err
			}()
			select {
			case <-ready:
			case <-time.After(time.Second):
				t.Fatal("TLS ClientHello was not received")
			}
			if tc.args.cancel {
				cancel()
			}
			select {
			case err = <-result:
			case <-time.After(time.Second):
				t.Fatal("TLS handshake did not stop")
			}
			assert.ErrorIs(t, err, tc.want)
			assert.Less(t, time.Since(started), 300*time.Millisecond)
		})
	}
}

func TestDialClearsConnectionDeadline(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_ = rw.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = rw.Write(append([]byte{0x81, 5}, []byte("after")...))
		_ = rw.Flush()
	}))
	t.Cleanup(server.Close)
	conf := server.Client().Transport.(*http.Transport).TLSClientConfig
	conn, err := socket.Dial(context.Background(), "wss"+strings.TrimPrefix(server.URL, "https"), conf,
		100*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	raw, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "after", string(raw))
}
