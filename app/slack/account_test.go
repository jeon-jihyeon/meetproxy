package slack_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/slack"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreToken(t *testing.T) {
	t.Parallel()
	type args struct {
		input string
		base  bool
	}
	type want struct {
		invalid, failed bool
		calls           int64
		user, token     string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"accepted", args{" xoxp-fixture\n", true}, want{calls: 1, user: "U1", token: "xoxp-fixture\n"}},
		{"wrong prefix", args{"other", true}, want{invalid: true, failed: true}},
		{"multiple tokens", args{"xoxp-a xoxp-b", true}, want{invalid: true, failed: true}},
		{"too long", args{"xoxp-" + strings.Repeat("a", 4092), true}, want{invalid: true, failed: true}},
		{"unsafe base", args{"xoxp-fixture", false}, want{failed: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			base := "https://example.com"
			if tc.args.base {
				base = srv.URL
			}
			data := t.TempDir()
			a := slack.New(slack.Config{Data: data, Base: base, Now: time.Unix(1893456000, 0)})
			got, err := a.StoreToken([]byte(tc.args.input))
			value, _ := store.NewDelivery(data).Token()
			assert.Equal(t, tc.want.failed, err != nil)
			assert.Equal(t, tc.want.invalid, err == slack.ErrInvalidToken)
			assert.Equal(t, tc.want.calls, calls.Load())
			assert.Equal(t, tc.want.user, got.User)
			assert.Equal(t, tc.want.token, string(value))
		})
	}
}

func TestAuthSnapshot(t *testing.T) {
	t.Parallel()
	type want struct{ token, user string }
	tcs := []struct {
		name string
		next string
		want want
	}{
		{"unchanged", "xoxp-old\n", want{"xoxp-old\n", "U1"}},
		{"replaced", "xoxp-new\n", want{"xoxp-new\n", "U2"}},
		{"removed", "", want{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			require.NoError(t, files.WriteToken([]byte("xoxp-old\n")))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				unlock, err := files.LockToken()
				if !assert.NoError(t, err) {
					return
				}
				defer unlock()
				if tc.next == "" {
					assert.NoError(t, files.RemoveToken())
				} else if tc.next != "xoxp-old\n" {
					assert.NoError(t, files.WriteToken([]byte(tc.next)))
					assert.NoError(t, files.WriteAuth(store.SlackAuth{User: "U2"}))
				}
				_, _ = w.Write([]byte(`{"ok":true,"user_id":"U1","team_id":"T1"}`))
			}))
			t.Cleanup(srv.Close)
			got, err := slack.New(slack.Config{Data: data, Base: srv.URL}).Auth(true)
			require.NoError(t, err)
			kept, _, err := files.Auth()
			require.NoError(t, err)
			token, _ := files.Token()
			assert.Equal(t, "U1", got.User)
			assert.Equal(t, tc.want.user, kept.User)
			assert.Equal(t, tc.want.token, string(token))
		})
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()
	type args struct {
		stored, replace bool
		revoke, auth    string
		failure         string
	}
	type want struct {
		result, token, user string
		health              bool
		oldAuth             int64
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"confirmed", args{true, false, `{"ok":true,"revoked":true}`,
			`{"ok":false,"error":"token_revoked"}`, "read failed"}, want{result: "revoked", oldAuth: 1}},
		{"unconfirmed", args{true, false, `{"ok":false,"error":"invalid_auth"}`,
			`{"ok":false,"error":"token_revoked"}`, "read failed"},
			want{result: "removed, not revoked: revoke the token under OAuth & Permissions of the app", oldAuth: 1}},
		{"replacement", args{true, true, `{"ok":true,"revoked":true}`, "", "read failed"},
			want{"not removed: another session stored a new token meanwhile", "xoxp-new\n", "U2", true, 0}},
		{"pause", args{true, false, `{"ok":true,"revoked":true}`,
			`{"ok":false,"error":"not_authed"}`, "collection paused by the user; resume with the source tool"},
			want{result: "revoked", health: true, oldAuth: 1}},
		{"missing", args{failure: "read failed"}, want{result: "no token", health: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			if tc.args.stored {
				require.NoError(t, files.WriteToken([]byte("xoxp-old\n")))
				require.NoError(t, files.WriteAuth(store.SlackAuth{User: "U1"}))
			}
			require.NoError(t, files.WriteHealth(store.Health{
				Sources: map[string]store.SourceHealth{"slack": {Error: tc.args.failure}},
			}))
			var oldAuth atomic.Int64
			var account *slack.Account
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.revoke" {
					if tc.args.replace {
						_, err := account.StoreToken([]byte("xoxp-new"))
						assert.NoError(t, err)
					}
					_, _ = w.Write([]byte(tc.args.revoke))
					return
				}
				if r.Header.Get("Authorization") == "Bearer xoxp-new" {
					_, _ = w.Write([]byte(`{"ok":true,"user_id":"U2","team_id":"T2"}`))
					return
				}
				oldAuth.Add(1)
				_, _ = w.Write([]byte(tc.args.auth))
			}))
			t.Cleanup(srv.Close)
			account = slack.New(slack.Config{Data: data, Base: srv.URL, Now: time.Unix(1893456000, 0)})
			got, err := account.Logout()
			require.NoError(t, err)
			token, _ := files.Token()
			auth, _, err := files.Auth()
			require.NoError(t, err)
			h, err := files.Health()
			require.NoError(t, err)
			_, kept := h.Sources["slack"]
			assert.Equal(t, tc.want.result, got)
			assert.Equal(t, tc.want.token, string(token))
			assert.Equal(t, tc.want.user, auth.User)
			assert.Equal(t, tc.want.health, kept)
			assert.Equal(t, tc.want.oldAuth, oldAuth.Load())
		})
	}
}

func TestRecordHealth(t *testing.T) {
	t.Parallel()
	type args struct {
		current, snapshot string
		byToken, paused   bool
		failure           string
	}
	type want struct {
		ok      bool
		failure string
		found   int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"same token", args{"xoxp-old\n", "xoxp-old\n", true, false, "new failure"},
			want{failure: "new failure", found: 2}},
		{"replacement", args{"xoxp-new\n", "xoxp-old\n", true, false, "new failure"}, want{ok: true}},
		{"removed", args{snapshot: "xoxp-old\n", byToken: true, failure: "new failure"}, want{ok: true}},
		{"no snapshot", args{current: "xoxp-old\n", byToken: true, failure: "new failure"}, want{ok: true}},
		{"connector", args{failure: "new failure"}, want{failure: "new failure", found: 2}},
		{"paused", args{paused: true, failure: "new failure"},
			want{failure: "collection paused by the user; resume with the source tool"}},
		{"success", args{}, want{ok: true, found: 2}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			if tc.args.current != "" {
				require.NoError(t, files.WriteToken([]byte(tc.args.current)))
			}
			require.NoError(t, files.WriteHealth(store.Health{
				Sources: map[string]store.SourceHealth{"slack": {OK: true}, "github": {Found: 3}},
				Hooks:   []store.HookFailure{{Hook: "fixture", Error: "kept"}},
			}))
			var snapshot []byte
			if tc.args.snapshot != "" {
				snapshot = []byte(tc.args.snapshot)
			}
			err := slack.New(slack.Config{Data: data, Now: time.Unix(1893456000, 0)}).RecordHealth(slack.Health{
				Token: snapshot, ByToken: tc.args.byToken, Paused: tc.args.paused, Error: tc.args.failure, Found: 2,
			})
			require.NoError(t, err)
			var h struct {
				Sources map[string]store.SourceHealth `json:"sources"`
				Hooks   []store.HookFailure           `json:"hooks"`
			}
			value, err := os.ReadFile(filepath.Join(data, "health.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(value, &h))
			assert.Equal(t, tc.want.ok, h.Sources["slack"].OK)
			assert.Equal(t, tc.want.failure, h.Sources["slack"].Error)
			assert.Equal(t, tc.want.found, h.Sources["slack"].Found)
			assert.Equal(t, 3, h.Sources["github"].Found)
			assert.Equal(t, []store.HookFailure{{Hook: "fixture", Error: "kept"}}, h.Hooks)
			assert.NotContains(t, string(value), "xoxp-")
		})
	}
}

func TestLogoutStorageFailures(t *testing.T) {
	t.Parallel()
	type args struct {
		block   string
		failure string
	}
	type want struct {
		failed, token bool
		revoke, auth  int64
		result        string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"unreadable token", args{block: "slack/token"}, want{failed: true, token: true}},
		{"token lock", args{block: "slack/token.lock"}, want{failed: true, token: true, revoke: 1}},
		{"partial removal", args{block: "slack/auth.json"}, want{failed: true, revoke: 1}},
		{"failed health lock", args{"health.lock", "read failed"}, want{failed: true, revoke: 1}},
		{"healthy source needs no cleanup", args{block: "health.lock"},
			want{revoke: 1, auth: 1, result: "revoked"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewDelivery(data)
			require.NoError(t, os.MkdirAll(filepath.Join(data, tc.args.block, "blocked"), 0o700))
			if tc.args.block != "slack/token" {
				require.NoError(t, files.WriteToken([]byte("xoxp-old\n")))
			}
			require.NoError(t, files.WriteHealth(store.Health{
				Sources: map[string]store.SourceHealth{"slack": {OK: tc.args.failure == "", Error: tc.args.failure}},
			}))
			var revoke, auth atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.revoke" {
					revoke.Add(1)
					_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
					return
				}
				auth.Add(1)
				_, _ = w.Write([]byte(`{"ok":false,"error":"token_revoked"}`))
			}))
			t.Cleanup(srv.Close)
			got, err := slack.New(slack.Config{Data: data, Base: srv.URL}).Logout()
			_, statErr := os.Stat(filepath.Join(data, "slack/token"))
			assert.Equal(t, tc.want.failed, err != nil)
			assert.Equal(t, tc.want.token, statErr == nil)
			assert.Equal(t, tc.want.result, got)
			assert.Equal(t, tc.want.revoke, revoke.Load())
			assert.Equal(t, tc.want.auth, auth.Load())
		})
	}
}

func TestRecordHealthSkipsUnneededWrite(t *testing.T) {
	t.Parallel()
	type args struct {
		found   int
		elapsed time.Duration
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"recent same result needs no lock", args{2, 9 * time.Minute}, false},
		{"new count needs the lock", args{3, 9 * time.Minute}, true},
		{"refresh boundary needs the lock", args{2, 10 * time.Minute}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			now := time.Unix(1893456000, 0).UTC()
			files := store.NewDelivery(data)
			require.NoError(t, files.WriteHealth(store.Health{
				Sources: map[string]store.SourceHealth{"slack": {OK: true, Found: 2, At: now}},
			}))
			require.NoError(t, os.Mkdir(filepath.Join(data, "health.lock"), 0o700))
			err := slack.New(slack.Config{Data: data, Now: now.Add(tc.args.elapsed)}).RecordHealth(slack.Health{
				Found: tc.args.found,
			})
			assert.Equal(t, tc.want, err != nil)
			h, err := files.Health()
			require.NoError(t, err)
			assert.Equal(t, store.SourceHealth{OK: true, Found: 2, At: now}, h.Sources["slack"])
		})
	}
}
