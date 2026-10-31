package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_SlackScopeResults(t *testing.T) {
	type want struct {
		token, status string
	}
	tcs := []struct {
		name    string
		granted []string
		want    want
	}{
		{"complete", slackScopes, want{`[]`, ""}},
		{"partial with duplicate and unknown", append([]string{"other:read", "channels:history"}, slackScopes[1:]...),
			want{`["search:read"]`, `["search:read"]`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("X-OAuth-Scopes", strings.Join(tc.granted, ","))
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			t.Setenv(slackAPIEnv, srv.URL)
			data := t.TempDir()
			var token bytes.Buffer
			code, err := run("slack", []string{"token", "--data", data}, "S1", time.Time{},
				strings.NewReader("xoxp-fixture"), &token)
			require.NoError(t, err)
			require.Zero(t, code)
			var result map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(token.Bytes(), &result))
			assert.Equal(t, tc.want.token, string(result["missing"]))
			assert.Equal(t, int64(1), calls.Load())
		})
	}
}
