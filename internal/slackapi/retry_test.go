package slackapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientLongRetryAfter(t *testing.T) {
	t.Parallel()
	for _, delay := range []string{"120", "9223372036854775807", "999999999999999999999999999999999999999"} {
		t.Run(delay, func(t *testing.T) {
			t.Parallel()
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Retry-After", delay)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			t.Cleanup(srv.Close)
			client, err := New(srv.URL, "neutral-test-token")
			require.NoError(t, err)
			waited := false
			client.sleep = func(time.Duration) { waited = true }
			_, _, err = client.RepliesPage("C1", "100", "100", "200", "")
			assert.ErrorContains(t, err, "retry_after:"+delay)
			assert.Equal(t, 1, calls)
			assert.False(t, waited)
		})
	}
}

// A Retry-After that would wait past the client's deadline fails at once, so a call never outlives its caller
func TestClientRetryAfterPastTheDeadline(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "neutral-test-token")
	require.NoError(t, err)
	waited := false
	client.sleep = func(time.Duration) { waited = true }

	_, _, err = client.Until(time.Now().Add(10*time.Second)).RepliesPage("C1", "100", "100", "200", "")

	assert.ErrorContains(t, err, "retry_after:30")
	assert.Equal(t, 1, calls)
	assert.False(t, waited)
}

// A call past the client's deadline never reaches Slack
func TestClientPastTheDeadline(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "neutral-test-token")
	require.NoError(t, err)

	_, _, err = client.Until(time.Now().Add(-time.Second)).RepliesPage("C1", "100", "100", "200", "")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, ErrPastDeadline, "a caller can tell nothing was sent")
	assert.Zero(t, calls)
}

// A Retry-After that fits before the deadline but leaves the retry less than its request time fails at once
func TestClientRetryAfterLeavesNoRoomToSend(t *testing.T) {
	t.Parallel()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "25")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "neutral-test-token")
	require.NoError(t, err)
	waited := false
	client.sleep = func(time.Duration) { waited = true }

	_, err = client.Until(time.Now().Add(40*time.Second)).Post("C1", "", "done")

	assert.ErrorContains(t, err, "ratelimited retry_after:25")
	assert.Equal(t, 1, calls)
	assert.False(t, waited)
}
