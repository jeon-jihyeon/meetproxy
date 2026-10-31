package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_SlackManifest(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	code, err := run("slack", []string{"--data", t.TempDir(), "manifest"}, "s1", time.Now(), nil, &out)
	require.NoError(t, err)
	require.Equal(t, 0, code)
	var got struct {
		Manifest struct {
			OAuth struct {
				Scopes struct {
					User []string `json:"user"`
				} `json:"scopes"`
			} `json:"oauth_config"`
		} `json:"manifest"`
		URL string `json:"url"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history", "users:read", "chat:write",
		"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}, got.Manifest.OAuth.Scopes.User)
	assert.True(t, strings.HasPrefix(got.URL, "https://api.slack.com/apps?new_app=1&manifest_json=%7B%22"), got.URL)
}

// Not parallel since the base comes from the environment
func TestRun_SlackRefusesAnotherHost(t *testing.T) {
	t.Setenv(slackAPIEnv, "https://evil.example/api")
	code, err := run("slack", []string{"--data", t.TempDir(), "token"}, "s1", time.Now(), strings.NewReader("xoxp-good"), &bytes.Buffer{})
	assert.Equal(t, exitFailed, code)
	assert.ErrorContains(t, err, "loopback")
}

// slack token and slack logout touch the token files only once the token.lock another session holds is free
// Not parallel since the fake Slack is named through the environment
func TestSlackTokenLock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth.revoke" {
			_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"url":"https://acme.slack.com/","user_id":"U1","team_id":"T1"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(slackAPIEnv, srv.URL)
	tcs := []struct {
		name   string
		args   []string
		stdin  string
		stored bool
	}{
		{"slack token", []string{"token"}, "xoxp-n3w", false},
		{"slack logout", []string{"logout"}, "", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			data := t.TempDir()
			require.NoError(t, os.MkdirAll(slackDir(data), 0o700))
			if tc.stored {
				require.NoError(t, os.WriteFile(tokenFile(data), []byte("xoxp-s3cret\n"), 0o600))
			}
			unlock, err := fileio.Lock(filepath.Join(slackDir(data), "token.lock"))
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() {
				var out bytes.Buffer
				_, err := run("slack", append(tc.args, "--data", data), "s1", modNow, strings.NewReader(tc.stdin), &out)
				done <- err
			}()

			select {
			case err := <-done:
				unlock()
				t.Fatalf("it ran past the held lock: %v", err)
			case <-time.After(300 * time.Millisecond):
			}
			assert.Equal(t, tc.stored, hasToken(data), "the token file stays as it was while the lock is held")
			unlock()

			require.NoError(t, <-done)
			assert.Equal(t, !tc.stored, hasToken(data))
		})
	}
}

// A live auth.test whose token was removed or replaced meanwhile writes no auth.json of the old token
// Not parallel since the fake Slack is named through the environment
func TestSlackWhoami_TokenGoneMeanwhile(t *testing.T) {
	for _, swap := range []bool{false, true} {
		t.Run(fmt.Sprint("swap ", swap), func(t *testing.T) {
			data := t.TempDir()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, os.Remove(tokenFile(data)))
				if swap {
					assert.NoError(t, os.WriteFile(tokenFile(data), []byte("xoxp-n3w\n"), 0o600))
				}
				_, _ = w.Write([]byte(`{"ok":true,"url":"https://acme.slack.com/","user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			t.Setenv(slackAPIEnv, srv.URL)
			require.NoError(t, os.MkdirAll(slackDir(data), 0o700))
			require.NoError(t, os.WriteFile(tokenFile(data), []byte("xoxp-s3cret\n"), 0o600))

			out := mustRun(t, data, "s1", modNow, step{"slack", []string{"whoami"}, ""})

			assert.Contains(t, out, `"user":"U1"`)
			assert.NoFileExists(t, authFile(data))
		})
	}
}
