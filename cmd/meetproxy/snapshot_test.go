package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackSnapshot(t *testing.T) {
	type want struct {
		calls                       int
		failed, empty, eyes, edited bool
	}
	tcs := []struct {
		name, mode string
		want       want
	}{
		{"full thread", "complete", want{2, false, false, true, true}},
		{"repeated cursor", "repeat", want{2, true, true, false, false}},
		{"page limit", "limit", want{50, true, true, false, false}},
		{"failed second page", "fail", want{2, true, true, false, false}},
		{"missing continuation", "more", want{1, true, true, false, false}},
		{"missing replies", "partial", want{1, true, true, false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, r.ParseForm())
				calls++
				assert.Equal(t, "1893456000.000001", r.Form.Get("ts"))
				if tc.mode == "fail" && calls == 2 {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "internal_error"})
					return
				}
				next := "page"
				if tc.mode == "complete" && calls == 2 {
					next = ""
				}
				if tc.mode == "limit" {
					next = strconv.Itoa(calls)
				}
				if tc.mode == "more" || tc.mode == "partial" {
					next = ""
				}
				ts := "1893456000.000001"
				if calls > 1 {
					ts = "1893456001.000001"
				}
				count := 1
				if tc.mode == "partial" {
					count = 2
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true,
					"has_more": tc.mode == "more",
					"messages": []any{map[string]any{
						"ts": ts, "reply_count": count, "text": "full neutral body", "edited": map[string]string{"ts": "2"},
						"reactions": []any{map[string]any{"name": "eyes", "count": 1, "users": []string{"U1"}}},
					}},
					"response_metadata": map[string]string{"next_cursor": next}})
			}))
			defer srv.Close()
			t.Setenv(slackAPIEnv, srv.URL)
			var out bytes.Buffer
			c := cli{data: t.TempDir(), out: &out}
			require.NoError(t, os.MkdirAll(filepath.Dir(tokenFile(c.data)), 0o700))
			require.NoError(t, os.WriteFile(tokenFile(c.data), []byte("xoxp-neutral-fixture"), 0o600))
			err := c.snapshot("https://acme.slack.com/archives/C1/p1893456000000001")
			assert.Equal(t, tc.want, want{calls, err != nil, out.Len() == 0,
				strings.Contains(out.String(), `"name":"eyes"`), strings.Contains(out.String(), `"edited":{"ts":"2"}`)})
		})
	}
}
