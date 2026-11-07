package socket

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type peer struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request, accept string) *peer {
	t.Helper()
	conn, rw, err := http.NewResponseController(w).Hijack()
	require.NoError(t, err)
	if accept == "" {
		accept = acceptKey(r.Header.Get("Sec-WebSocket-Key"))
	}
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n")
	require.NoError(t, err)
	require.NoError(t, rw.Flush())
	return &peer{conn, rw}
}

func (p *peer) send(fin bool, op byte, payload []byte) error {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	b := []byte{b0}
	switch {
	case len(payload) < 126:
		b = append(b, byte(len(payload)))
	case len(payload) <= 0xffff:
		b = binary.BigEndian.AppendUint16(append(b, 126), uint16(len(payload)))
	default:
		b = binary.BigEndian.AppendUint64(append(b, 127), uint64(len(payload)))
	}
	if _, err := p.rw.Write(append(b, payload...)); err != nil {
		return err
	}
	return p.rw.Flush()
}

type clientFrame struct {
	op     byte
	masked bool
	raw    []byte
	data   []byte
}

func (p *peer) recv() (clientFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(p.rw, h[:]); err != nil {
		return clientFrame{}, err
	}
	n := int(h[1] & 0x7f)
	if n == 126 {
		var b [2]byte
		if _, err := io.ReadFull(p.rw, b[:]); err != nil {
			return clientFrame{}, err
		}
		n = int(binary.BigEndian.Uint16(b[:]))
	}
	f := clientFrame{op: h[0] & 0x0f, masked: h[1]&0x80 != 0}
	var key [4]byte
	if f.masked {
		if _, err := io.ReadFull(p.rw, key[:]); err != nil {
			return clientFrame{}, err
		}
	}
	f.raw = make([]byte, n)
	if _, err := io.ReadFull(p.rw, f.raw); err != nil {
		return clientFrame{}, err
	}
	f.data = make([]byte, n)
	for i := range f.raw {
		f.data[i] = f.raw[i] ^ key[i%4]
	}
	return f, nil
}

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func wsServer(t *testing.T, accept string, serve func(p *peer)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := upgrade(t, w, r, accept)
		defer func() { _ = p.conn.Close() }()
		serve(p)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDial_Handshake(t *testing.T) {
	t.Parallel()
	t.Run("a matching accept connects", func(t *testing.T) {
		t.Parallel()
		srv := wsServer(t, "", func(p *peer) { _, _ = p.recv() })
		c, err := Dial(context.Background(), wsURL(srv), nil, 5*time.Second)
		require.NoError(t, err)
		require.NoError(t, c.Close())
	})
	t.Run("wss connects over TLS", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := upgrade(t, w, r, "")
			defer func() { _ = p.conn.Close() }()
			_ = p.send(true, opText, []byte("secure"))
			_, _ = p.recv()
		}))
		t.Cleanup(srv.Close)
		conf := srv.Client().Transport.(*http.Transport).TLSClientConfig
		c, err := Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https"), conf, 5*time.Second)
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		msg, err := c.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, "secure", string(msg))
	})
	t.Run("a wrong accept fails", func(t *testing.T) {
		t.Parallel()
		srv := wsServer(t, "bm90IHRoZSBrZXk=", func(*peer) {})
		_, err := Dial(context.Background(), wsURL(srv), nil, 5*time.Second)
		assert.ErrorContains(t, err, "Sec-WebSocket-Accept")
	})
	t.Run("an http scheme fails", func(t *testing.T) {
		t.Parallel()
		_, err := Dial(context.Background(), "http://127.0.0.1:1/", nil, time.Second)
		assert.ErrorContains(t, err, "ws or wss")
	})
}

func TestReadMessage(t *testing.T) {
	t.Parallel()
	got := make(chan clientFrame, 4)
	srv := wsServer(t, "", func(p *peer) {
		_ = p.send(false, opText, []byte("hel"))
		_ = p.send(true, opPing, []byte("p1"))
		_ = p.send(false, opContinue, []byte("l"))
		_ = p.send(true, opContinue, []byte("o"))
		for range 2 {
			f, err := p.recv()
			if err != nil {
				return
			}
			got <- f
		}
		_ = p.send(true, opClose, []byte{0x03, 0xe8})
		f, _ := p.recv()
		got <- f
	})
	c, err := Dial(context.Background(), wsURL(srv), nil, 5*time.Second)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	msg, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "hello", string(msg))
	pong := <-got
	assert.Equal(t, []any{opPong, true, "p1"}, []any{pong.op, pong.masked, string(pong.data)})

	require.NoError(t, c.WriteText([]byte("abcdef")))
	text := <-got
	assert.Equal(t, []any{opText, true, "abcdef"}, []any{text.op, text.masked, string(text.data)})
	assert.NotEqual(t, "abcdef", string(text.raw))

	_, err = c.ReadMessage()
	assert.ErrorIs(t, err, ErrClosed)
	echoed := <-got
	assert.Equal(t, []any{opClose, []byte{0x03, 0xe8}}, []any{echoed.op, echoed.data})
}

func TestReadMessage_Rejects(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		send func(p *peer)
		want string
	}{
		{"a masked server frame", func(p *peer) {
			_, _ = p.rw.Write([]byte{0x81, 0x81, 1, 2, 3, 4, 'x'})
			_ = p.rw.Flush()
		}, "masked"},
		{"a continuation without a message", func(p *peer) { _ = p.send(true, opContinue, []byte("x")) }, "continuation"},
		{"a fragmented ping", func(p *peer) { _ = p.send(false, opPing, nil) }, "control"},
		{"a new message inside a fragmented one", func(p *peer) {
			_ = p.send(false, opText, []byte("a"))
			_ = p.send(true, opText, []byte("b"))
		}, "inside"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := wsServer(t, "", func(p *peer) {
				tc.send(p)
				_, _ = p.recv()
			})
			c, err := Dial(context.Background(), wsURL(srv), nil, 5*time.Second)
			require.NoError(t, err)
			defer func() { _ = c.Close() }()
			_, err = c.ReadMessage()
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestReadMessage_Timeout(t *testing.T) {
	t.Parallel()
	srv := wsServer(t, "", func(p *peer) { _, _ = p.recv() })
	c, err := Dial(context.Background(), wsURL(srv), nil, 5*time.Second)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	c.readTimeout = 50 * time.Millisecond
	_, err = c.ReadMessage()
	var ne net.Error
	require.ErrorAs(t, err, &ne)
	assert.True(t, ne.Timeout())
}

type slackServer struct {
	*httptest.Server
	opens atomic.Int32
}

func newSlackServer(t *testing.T, token string, script func(n int32, p *peer)) *slackServer {
	t.Helper()
	s := &slackServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			return
		}
		s.opens.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": wsURL(s.Server) + "/link/?ticket=t"})
	})
	mux.HandleFunc("GET /link/", func(w http.ResponseWriter, r *http.Request) {
		p := upgrade(t, w, r, "")
		defer func() { _ = p.conn.Close() }()
		script(s.opens.Load(), p)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func encodedEnvelope(id, typ string) []byte {
	value, _ := json.Marshal(map[string]any{
		"envelope_id": id, "type": typ, "payload": map[string]string{"k": id},
	})
	return value
}

func TestSubscriptionFailureBudget(t *testing.T) {
	t.Parallel()
	type want struct {
		opens int32
		calls int32
	}
	tcs := []struct {
		name     string
		progress bool
		want     want
	}{
		{"hello never hides a failed receipt", false, want{2, 2}},
		{"an ACK resets the failure count", true, want{3, 3}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			var badACK atomic.Bool
			srv := newSlackServer(t, "xapp-fixture", func(n int32, p *peer) {
				_ = p.send(true, opText, []byte(`{"type":"hello"}`))
				_ = p.send(true, opText, encodedEnvelope(strconv.Itoa(int(n)), "events_api"))
				frame, err := p.recv()
				if err == nil && (!tc.progress || n != 2) && frame.op == opText {
					badACK.Store(true)
				}
			})
			client, err := New(srv.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { assert.NoError(t, client.Close()) }()
			client.MinBackoff, client.MaxBackoff, client.MaxFailures = time.Millisecond, 2*time.Millisecond, 2
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			for {
				var delivery Delivery
				delivery, err = client.Next(ctx)
				if err != nil {
					break
				}
				if n := calls.Add(1); tc.progress && n == 2 {
					require.NoError(t, client.Ack(ctx, delivery.Receipt))
					continue
				}
				client.mu.Lock()
				conn := client.conn
				client.mu.Unlock()
				require.NoError(t, conn.Close())
				require.Eventually(t, func() bool {
					client.mu.Lock()
					defer client.mu.Unlock()
					return client.receipt == ""
				}, time.Second, time.Millisecond)
			}

			assert.ErrorIs(t, err, ErrRetries)
			assert.Equal(t, tc.want.opens, srv.opens.Load())
			assert.Equal(t, tc.want.calls, calls.Load())
			assert.False(t, badACK.Load())
			assert.NotContains(t, err.Error(), "private")
		})
	}
}

func TestSubscriptionRefresh(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv := newSlackServer(t, "xapp-fixture", func(n int32, p *peer) {
		if n == 1 {
			_ = p.send(true, opText, []byte(`{"type":"disconnect","reason":"refresh_requested"}`))
			_, _ = p.recv()
			return
		}
		cancel()
		_, _ = p.recv()
	})
	client, err := New(srv.URL+"/api", "xapp-fixture")
	require.NoError(t, err)
	defer func() { assert.NoError(t, client.Close()) }()
	client.MaxFailures = 1

	_, err = client.Next(ctx)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int32(2), srv.opens.Load())
}

func TestSubscriptionDisconnectReasons(t *testing.T) {
	t.Parallel()
	type args struct {
		reason   string
		failures int
		planned  bool
	}
	type want struct {
		opens int32
		err   error
		delay time.Duration
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"planned warning", args{"warning", 1, true}, want{2, context.Canceled, 0}},
		{"planned refresh", args{"refresh_requested", 1, true}, want{2, context.Canceled, 0}},
		{"disabled link", args{"link_disabled", 1, false}, want{1, ErrAuth, 0}},
		{"unknown reason", args{"untrusted fixture", 2, false}, want{2, ErrRetries, 40 * time.Millisecond}},
		{"missing reason", args{"", 2, false}, want{2, ErrRetries, 40 * time.Millisecond}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			srv := newSlackServer(t, "xapp-fixture", func(n int32, p *peer) {
				if tc.args.planned && n == 2 || n > 2 {
					cancel()
					_, _ = p.recv()
					return
				}
				body, err := json.Marshal(map[string]string{"type": "disconnect", "reason": tc.args.reason})
				require.NoError(t, err)
				_ = p.send(true, opText, body)
				_, _ = p.recv()
			})
			client, err := New(srv.URL+"/api", "xapp-fixture")
			require.NoError(t, err)
			defer func() { assert.NoError(t, client.Close()) }()
			client.MaxFailures = tc.args.failures
			client.MinBackoff, client.MaxBackoff = 40*time.Millisecond, 40*time.Millisecond
			started := time.Now()
			_, err = client.Next(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.opens, srv.opens.Load())
			assert.GreaterOrEqual(t, time.Since(started), tc.want.delay)
			assert.NotContains(t, err.Error(), "untrusted fixture")
		})
	}
}

func TestSubscriptionRefusals(t *testing.T) {
	t.Parallel()
	type args struct {
		status int
		body   string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"auth", args{http.StatusOK, `{"ok":false,"error":"invalid_auth"}`}, ErrAuth},
		{"rate", args{http.StatusTooManyRequests, `{"ok":false}`}, ErrRate},
		{"failed open", args{http.StatusServiceUnavailable, "private response"}, ErrRetries},
		{"unsafe socket", args{http.StatusOK, `{"ok":true,"url":"wss://example.com/link"}`}, ErrRetries},
		{"malformed socket", args{http.StatusOK, `{"ok":true,"url":"::private-ticket"}`}, ErrRetries},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opens atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				opens.Add(1)
				w.WriteHeader(tc.args.status)
				_, _ = w.Write([]byte(tc.args.body))
			}))
			defer srv.Close()
			client, err := New(srv.URL, "xapp-fixture")
			require.NoError(t, err)
			defer func() { assert.NoError(t, client.Close()) }()
			client.MaxFailures = 1

			_, err = client.Next(context.Background())

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, int32(1), opens.Load())
			assert.NotContains(t, err.Error(), "private")
			assert.NotContains(t, err.Error(), "example.com")
		})
	}
}

func TestSubscriptionRefusesRedirect(t *testing.T) {
	t.Parallel()
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization") != "")
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client, err := New(srv.URL, "xapp-fixture")
	require.NoError(t, err)
	defer func() { assert.NoError(t, client.Close()) }()
	client.MaxFailures = 1

	_, err = client.Next(context.Background())

	assert.ErrorIs(t, err, ErrRetries)
	assert.False(t, leaked.Load())
}

func TestNew(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		base string
		want error
	}{
		{"default", "", nil},
		{"explicit Slack", DefaultBase, nil},
		{"loopback", "http://127.0.0.1/api", nil},
		{"other host", "https://example.com/api", ErrBase},
		{"userinfo", "http://user@127.0.0.1/api", ErrBase},
		{"other scheme", "ftp://127.0.0.1/api", ErrBase},
		{"fragment", "http://127.0.0.1/api#fragment", ErrBase},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tc.base, "xapp-fixture")

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
