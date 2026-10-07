package slackapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fake Slack that answers each method with the function of its form
// It records each call as the method and its form
type fake struct {
	mu      sync.Mutex
	calls   []string
	answers map[string]func(r *http.Request) (int, any)
}

func (f *fake) client(t *testing.T) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[1:]
		require.NoError(t, r.ParseForm())
		f.mu.Lock()
		f.calls = append(f.calls, method+" "+r.Form.Encode())
		f.mu.Unlock()
		assert.Equal(t, "Bearer xoxp-1", r.Header.Get("Authorization"))
		answer, ok := f.answers[method]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		code, body := answer(r)
		w.Header().Set("X-OAuth-Scopes", "search:read, users:read")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "xoxp-1")
	require.NoError(t, err)
	c.sleep = func(time.Duration) {}
	return c
}

func ok(body map[string]any) (int, any) {
	body["ok"] = true
	return http.StatusOK, body
}

func TestNew(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		base string
		want bool
	}{
		{"slack.com by default", "", true},
		{"a loopback address", "http://127.0.0.1:9", true},
		{"localhost", "http://localhost:9/api", true},
		{"an IPv6 loopback", "http://[::1]:9", true},
		{"another host is refused", "https://evil.example/api", false},
		{"slack.com named again is refused", "https://slack.com/api", false},
		{"a loopback name in the path does not count", "https://evil.example/127.0.0.1", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tc.base, "xoxp-1")
			assert.Equal(t, tc.want, err == nil)
			if err != nil {
				assert.ErrorIs(t, err, ErrBase)
			}
		})
	}
}

func TestAuthTest(t *testing.T) {
	t.Parallel()
	f := &fake{answers: map[string]func(*http.Request) (int, any){
		"auth.test": func(*http.Request) (int, any) {
			return ok(map[string]any{"url": "https://acme.slack.com/", "user_id": "U1", "team_id": "T1"})
		},
	}}
	a, err := f.client(t).AuthTest()
	require.NoError(t, err)
	assert.Equal(t, Auth{User: "U1", Team: "T1", Host: "acme.slack.com", Scopes: []string{"search:read", "users:read"}}, a)
}

func TestErrors(t *testing.T) {
	t.Parallel()
	type want struct {
		code   string
		needed string
		calls  int
	}
	tcs := []struct {
		name   string
		answer func(n int) (int, any)
		want   want
	}{
		{"a missing scope names the scope", func(int) (int, any) {
			return http.StatusOK, map[string]any{"ok": false, "error": "missing_scope", "needed": "search:read"}
		}, want{"missing_scope", "search:read", 1}},
		{"a revoked token fails at once", func(int) (int, any) {
			return http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"}
		}, want{"invalid_auth", "", 1}},
		{"a rate limit twice fails", func(int) (int, any) {
			return http.StatusTooManyRequests, map[string]any{"ok": false, "error": "ratelimited"}
		}, want{"ratelimited", "", 2}},
		{"a rate limit once is waited out", func(n int) (int, any) {
			if n == 1 {
				return http.StatusTooManyRequests, map[string]any{}
			}
			return ok(map[string]any{"user_id": "U1"})
		}, want{"", "", 2}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := 0
			f := &fake{answers: map[string]func(*http.Request) (int, any){
				"auth.test": func(*http.Request) (int, any) { n++; return tc.answer(n) },
			}}
			c := f.client(t)
			var waited []time.Duration
			c.sleep = func(d time.Duration) { waited = append(waited, d) }

			_, err := c.AuthTest()

			var se *Error
			got := want{calls: len(f.calls)}
			if errors.As(err, &se) {
				got.code, got.needed = se.Code, se.Needed
			}
			assert.Equal(t, tc.want, got)
			if tc.want.calls == 2 {
				assert.Equal(t, []time.Duration{7 * time.Second}, waited)
			}
		})
	}
}

func TestSearchMentions(t *testing.T) {
	t.Parallel()
	pages := map[string][]map[string]any{
		"1": {
			{"ts": "1893456000.000001", "user": "U2", "text": "old"},
			{"ts": "1893456100.000001", "user": "U2", "text": "a", "permalink": "https://w.slack.com/archives/C1/p1893456100000001", "channel": map[string]any{"id": "C1"}},
		},
		"2": {
			{"ts": "1893456200.000001", "user": "", "bot_id": "B1", "text": "bot"},
			{"ts": "1893456300.000001", "user": "U3", "text": "b"},
		},
	}
	f := &fake{answers: map[string]func(*http.Request) (int, any){
		"search.messages": func(r *http.Request) (int, any) {
			return ok(map[string]any{"messages": map[string]any{"matches": pages[r.Form.Get("page")], "paging": map[string]any{"pages": 2}}})
		},
	}}

	got, err := f.client(t).SearchMentions("U1", "1893456050")

	require.NoError(t, err)
	texts := []string{}
	for _, m := range got {
		texts = append(texts, m.Text)
	}
	assert.Equal(t, []string{"a", "b"}, texts)
	assert.Equal(t, "C1", got[0].Channel.Id)
	assert.Equal(t, []string{
		"search.messages count=100&page=1&query=%3C%40U1%3E+after%3A2029-12-31&sort=timestamp&sort_dir=asc",
		"search.messages count=100&page=2&query=%3C%40U1%3E+after%3A2029-12-31&sort=timestamp&sort_dir=asc",
	}, f.calls)
}

func TestHistory(t *testing.T) {
	t.Parallel()
	type want struct {
		pages     int
		truncated bool
	}
	last := func(c string) string {
		if c == "" {
			return "p2"
		}
		return ""
	}
	tcs := []struct {
		name string
		// The next cursor after the page of cursor, empty for the last page
		next func(cursor string) string
		want want
	}{
		{"reads every page", last, want{2, false}},
		{"stops at the bound and says so", func(string) string { return "more" }, want{MaxPages, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fake{answers: map[string]func(*http.Request) (int, any){
				"conversations.history": func(r *http.Request) (int, any) {
					return ok(map[string]any{
						"messages":          []map[string]any{{"ts": "1.0", "text": r.Form.Get("cursor")}},
						"response_metadata": map[string]any{"next_cursor": tc.next(r.Form.Get("cursor"))},
					})
				},
			}}

			got, truncated, err := f.client(t).History("C9", "1893456000")

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{len(got), truncated})
		})
	}
}

func TestPostAndUserInfo(t *testing.T) {
	t.Parallel()
	f := &fake{answers: map[string]func(*http.Request) (int, any){
		"chat.postMessage": func(*http.Request) (int, any) { return ok(map[string]any{}) },
		"users.info": func(*http.Request) (int, any) {
			return ok(map[string]any{"user": map[string]any{"id": "U2", "team_id": "T1", "name": "kai", "real_name": "Kai", "is_ultra_restricted": true}})
		},
	}}
	c := f.client(t)

	require.NoError(t, c.Post("C1", "1.2", "hi"))
	u, err := c.UserInfo("U2")

	require.NoError(t, err)
	assert.Equal(t, User{Id: "U2", Team: "T1", Name: "Kai", Restricted: true}, u)
	assert.Equal(t, []string{"chat.postMessage channel=C1&text=hi&thread_ts=1.2", "users.info user=U2"}, f.calls)
}
