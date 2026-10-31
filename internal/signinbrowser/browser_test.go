package signinbrowser_test

import (
	"context"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/signinbrowser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientInspect(t *testing.T) {
	t.Parallel()
	type want struct {
		single, known, loopback bool
		origin                  string
	}
	tcs := []struct {
		name, out string
		want      want
	}{
		{"one tab", "1 http://localhost:8123/callback", want{true, true, true, signinbrowser.Origin("http://localhost:8123")}},
		{"leading zero", "01 http://localhost:8123/callback", want{false, true, true, signinbrowser.Origin("http://localhost:8123")}},
		{"many tabs", "12 http://localhost:8123/callback", want{false, true, true, signinbrowser.Origin("http://localhost:8123")}},
		{"gone", "gone", want{}},
		{"user page", "1 https://example.com", want{true, true, false, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := signinbrowser.New(func(context.Context, string) (string, error) { return tc.out, nil })
			page, err := client.Inspect(context.Background(), "Whale", "12")
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{page.Single, page.Known, page.Loopback, page.Origin})
		})
	}
}

func TestClientRejectsInvalidRecord(t *testing.T) {
	t.Parallel()
	type args struct{ browser, id string }
	tcs := []struct {
		name string
		args args
	}{
		{"unknown browser", args{"Safari", "12"}},
		{"quoted id", args{"Whale", `12"`}},
		{"empty id", args{"Whale", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			client := signinbrowser.New(func(context.Context, string) (string, error) { calls++; return "gone", nil })
			_, readErr := client.Read(context.Background(), tc.args.browser, tc.args.id)
			_, inspectErr := client.Inspect(context.Background(), tc.args.browser, tc.args.id)
			gone, closeErr := client.Close(context.Background(), tc.args.browser, tc.args.id)
			assert.ErrorIs(t, readErr, signinbrowser.ErrScript)
			assert.ErrorIs(t, inspectErr, signinbrowser.ErrScript)
			assert.ErrorIs(t, closeErr, signinbrowser.ErrScript)
			assert.False(t, gone)
			assert.Zero(t, calls)
		})
	}
}
