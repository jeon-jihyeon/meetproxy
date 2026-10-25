package slackapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
)

func TestClientMentionPage(t *testing.T) {
	t.Parallel()
	type args struct {
		after, until, cursor string
		fail                 bool
	}
	type want struct {
		next   string
		failed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"first page", args{"1893456000", "1893456600", "", false}, want{"2", false}},
		{"last page", args{"1893456000", "1893456600", "2", false}, want{"", false}},
		{"invalid start", args{"bad", "1893456600", "", false}, want{"", true}},
		{"invalid end", args{"1893456000", "bad", "", false}, want{"", true}},
		{"invalid page", args{"1893456000", "1893456600", "bad", false}, want{"", true}},
		{"negative page", args{"1893456000", "1893456600", "-1", false}, want{"", true}},
		{"permission failure", args{"1893456000", "1893456600", "", true}, want{"", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "<@U1> after:2029-12-31 before:2030-01-02", r.Form.Get("query"))
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": !tc.args.fail, "error": "missing_scope",
					"messages": map[string]any{"matches": []any{}, "paging": map[string]int{"pages": 2}}})
			}))
			t.Cleanup(srv.Close)
			client, err := slackapi.New(srv.URL, "neutral-test-token")
			require.NoError(t, err)
			_, next, err := client.MentionPage("U1", tc.args.after, tc.args.until, tc.args.cursor)
			assert.Equal(t, tc.want, want{next, err != nil})
		})
	}
}

func TestClientHistoryPage(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "continuation", true: "expired cursor"}[fail], func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, []string{"C1", "100", "200", "page-2"},
					[]string{r.Form.Get("channel"), r.Form.Get("oldest"), r.Form.Get("latest"), r.Form.Get("cursor")})
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": !fail, "error": "invalid_cursor",
					"messages": []any{}, "response_metadata": map[string]string{"next_cursor": "page-3"}})
			}))
			t.Cleanup(srv.Close)
			client, err := slackapi.New(srv.URL, "neutral-test-token")
			require.NoError(t, err)
			_, _, err = client.HistoryPage("C1", "100", "200", "page-2")
			assert.Equal(t, fail, err != nil)
		})
	}
}
