package slack_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/app/slack"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreTokenScopes(t *testing.T) {
	t.Parallel()
	required := []string{"search:read", "channels:history", "groups:history", "im:history", "mpim:history",
		"users:read", "chat:write", "reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}
	tcs := []struct {
		name             string
		granted, missing []string
	}{
		{"none", nil, required},
		{"complete", required, []string{}},
		{"partial in another order", []string{"chat:write", "search:read"}, []string{
			"channels:history", "groups:history", "im:history", "mpim:history", "users:read",
			"reactions:write", "channels:read", "groups:read", "im:read", "mpim:read"}},
		{"duplicate and unknown", []string{"search:read", "search:read", "other:read"}, required[1:]},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-OAuth-Scopes", strings.Join(tc.granted, ","))
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			data := t.TempDir()
			files := store.NewDelivery(data)
			a := slack.New(slack.Config{Data: data, Base: srv.URL})
			got, err := a.StoreToken([]byte("xoxp-fixture"))
			require.NoError(t, err)
			value, err := json.Marshal(got)
			require.NoError(t, err)
			var result struct{ Missing []string }
			require.NoError(t, json.Unmarshal(value, &result))
			assert.Equal(t, tc.missing, result.Missing)
			record, found, err := files.Auth()
			require.NoError(t, err)
			require.True(t, found)
			stored, err := json.Marshal(record)
			require.NoError(t, err)
			assert.NotContains(t, strings.ToLower(string(stored)), "missing")
		})
	}
}

func TestAuthScopes(t *testing.T) {
	t.Parallel()
	type args struct {
		live    bool
		granted []string
	}
	type want struct {
		missing []string
		calls   int64
	}
	partial := slack.RequiredScopes()[1:]
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"cached complete", args{false, slack.RequiredScopes()}, want{[]string{}, 0}},
		{"cached partial", args{false, []string{"search:read", "search:read", "other:read"}}, want{partial, 0}},
		{"live complete", args{true, slack.RequiredScopes()}, want{[]string{}, 1}},
		{"live partial", args{true, []string{"search:read", "search:read", "other:read"}}, want{partial, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			require.NoError(t, files.WriteToken([]byte("xoxp-fixture")))
			require.NoError(t, files.WriteAuth(store.SlackAuth{User: "U1", Scopes: tc.args.granted}))
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("X-OAuth-Scopes", strings.Join(tc.args.granted, ","))
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			got, err := slack.New(slack.Config{Data: data, Base: srv.URL}).Auth(tc.args.live)
			require.NoError(t, err)
			assert.Equal(t, tc.want.missing, got.Missing)
			assert.Equal(t, tc.want.calls, calls.Load())
		})
	}
}

func TestStoredAuth(t *testing.T) {
	t.Parallel()
	type args struct {
		stored  bool
		granted []string
	}
	type want struct {
		found   bool
		missing []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"complete", args{true, slack.RequiredScopes()}, want{true, []string{}}},
		{"partial", args{true, []string{"search:read", "search:read", "other:read"}},
			want{true, slack.RequiredScopes()[1:]}},
		{"empty", args{stored: true}, want{true, slack.RequiredScopes()}},
		{"missing", args{}, want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			require.NoError(t, files.WriteToken([]byte("xoxp-fixture")))
			if tc.args.stored {
				require.NoError(t, files.WriteAuth(store.SlackAuth{User: "U1", Scopes: tc.args.granted}))
			}
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			got, found, err := slack.New(slack.Config{Data: data, Base: srv.URL}).StoredAuth()
			require.NoError(t, err)
			assert.Equal(t, tc.want.found, found)
			assert.Equal(t, tc.want.missing, got.Missing)
			assert.Zero(t, calls.Load())
		})
	}
}
