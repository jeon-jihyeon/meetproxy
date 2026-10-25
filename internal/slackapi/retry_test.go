package slackapi

import (
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
