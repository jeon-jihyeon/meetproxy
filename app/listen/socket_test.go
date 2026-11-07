package listen_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/listen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketSource(t *testing.T) {
	t.Parallel()
	type args struct {
		tokenMode   os.FileMode
		runtimeMode os.FileMode
		token       string
	}
	type want struct {
		err   error
		calls int32
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"secure refusal releases the lock", args{0o600, 0o700, "xapp-fixture"}, want{listen.ErrAuth, 2}},
		{"token permissions", args{0o644, 0o700, "xapp-fixture"}, want{listen.ErrSecurity, 0}},
		{"runtime permissions", args{0o600, 0o755, "xapp-fixture"}, want{listen.ErrSecurity, 0}},
		{"wrong token type", args{0o600, 0o700, "xoxb-fixture"}, want{listen.ErrSecurity, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			token := filepath.Join(dir, "app-token")
			require.NoError(t, os.WriteFile(token, []byte(tc.args.token), tc.args.tokenMode))
			runtime := filepath.Join(dir, "runtime")
			require.NoError(t, os.Mkdir(runtime, tc.args.runtimeMode))
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, "Bearer xapp-fixture", r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			}))
			defer server.Close()
			source, err := listen.NewSocket(listen.SocketConfig{
				Directory: runtime, TokenFile: token, APIBase: server.URL, Failures: 1,
				BotUser: "U1",
			})
			require.NoError(t, err)
			_, first := source.Next(context.Background())
			require.NoError(t, source.Close())
			source, err = listen.NewSocket(listen.SocketConfig{
				Directory: runtime, TokenFile: token, APIBase: server.URL, Failures: 1, BotUser: "U1",
			})
			require.NoError(t, err)
			_, second := source.Next(context.Background())
			require.NoError(t, source.Close())

			assert.ErrorIs(t, first, tc.want.err)
			assert.ErrorIs(t, second, tc.want.err)
			assert.Equal(t, tc.want.calls, calls.Load())
		})
	}
}

func TestSocketCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	source, err := listen.NewSocket(listen.SocketConfig{
		Directory: runtime, TokenFile: filepath.Join(dir, "missing-token"), BotUser: "U1",
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = source.Next(ctx)
	require.NoError(t, source.Close())

	assert.ErrorIs(t, err, context.Canceled)
	_, err = os.Stat(runtime)
	assert.True(t, os.IsNotExist(err))
}

func TestSocketConfig(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		config listen.SocketConfig
	}{
		{"relative runtime", listen.SocketConfig{Directory: "runtime", TokenFile: "/tmp/app-token"}},
		{"relative token", listen.SocketConfig{Directory: "/tmp/runtime", TokenFile: "app-token"}},
		{"negative budget", listen.SocketConfig{Directory: "/tmp/runtime", TokenFile: "/tmp/app-token", Failures: -1}},
		{"large budget", listen.SocketConfig{Directory: "/tmp/runtime", TokenFile: "/tmp/app-token", Failures: 101}},
		{"missing bot", listen.SocketConfig{Directory: "/tmp/runtime", TokenFile: "/tmp/app-token"}},
		{"invalid bot", listen.SocketConfig{Directory: "/tmp/runtime", TokenFile: "/tmp/app-token", BotUser: "bad"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.name != "missing bot" && tc.name != "invalid bot" {
				tc.config.BotUser = "U1"
			}
			_, err := listen.NewSocket(tc.config)

			assert.ErrorIs(t, err, listen.ErrConfig)
		})
	}
}

func TestSocketRateLimit(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"seconds", "30", 30 * time.Second},
		{"missing", "", 0},
		{"invalid", "later", 0},
		{"overflow", "99999999999999999999999", 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			token := filepath.Join(dir, "app-token")
			require.NoError(t, os.WriteFile(token, []byte("xapp-fixture"), 0o600))
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.value)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()
			source, err := listen.NewSocket(listen.SocketConfig{
				Directory: filepath.Join(dir, "runtime"), TokenFile: token, APIBase: srv.URL,
				BotUser: "U1",
			})
			require.NoError(t, err)

			_, err = source.Next(context.Background())
			require.NoError(t, source.Close())

			var rate *listen.RateLimitError
			require.ErrorAs(t, err, &rate)
			assert.ErrorIs(t, err, listen.ErrRate)
			assert.Equal(t, tc.want, rate.RetryAfter)
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestSocketClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	token := filepath.Join(dir, "app-token")
	require.NoError(t, os.WriteFile(token, []byte("xapp-fixture"), 0o600))
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	source, err := listen.NewSocket(listen.SocketConfig{
		Directory: filepath.Join(dir, "runtime"), TokenFile: token, APIBase: srv.URL, BotUser: "U1",
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := source.Next(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}
	assert.NoError(t, source.Close())
	assert.NoError(t, source.Close())
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not release Next")
	}
	_, err = source.Next(context.Background())
	assert.ErrorIs(t, err, listen.ErrClosed)
	assert.ErrorIs(t, source.Ack(context.Background(), "opaque"), listen.ErrClosed)
}
