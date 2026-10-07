package main

import (
	"bytes"
	"encoding/json"

	"strings"
	"testing"
	"time"

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
	assert.Equal(t, []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history", "users:read", "chat:write", "reactions:write"}, got.Manifest.OAuth.Scopes.User)
	assert.True(t, strings.HasPrefix(got.URL, "https://api.slack.com/apps?new_app=1&manifest_json=%7B%22"), got.URL)
}

// Not parallel since the base comes from the environment
func TestRun_SlackRefusesAnotherHost(t *testing.T) {
	t.Setenv(slackAPIEnv, "https://evil.example/api")
	code, err := run("slack", []string{"--data", t.TempDir(), "token"}, "s1", time.Now(), strings.NewReader("xoxp-good"), &bytes.Buffer{})
	assert.Equal(t, exitFailed, code)
	assert.ErrorContains(t, err, "loopback")
}
