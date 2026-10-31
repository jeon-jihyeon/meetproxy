package signin_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/app/signin"
	"github.com/jeon-jihyeon/meetproxy/internal/signinbrowser"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceWaitRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	type args struct {
		address, close string
		failure        error
	}
	type want struct {
		word string
		kept bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"closed", args{"http://localhost:8123/callback", "gone", nil}, want{"closed", false}},
		{"owned still open", args{"http://localhost:8123/callback", "open", nil}, want{"not closed", true}},
		{"failed close", args{"http://localhost:8123/callback", "", signin.ErrBrowserScript}, want{"not closed", true}},
		{"other page", args{"http://localhost:9000/app", "", nil}, want{"closed", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewSignins(data)
			require.NoError(t, files.Write("s1", store.Signin{Browser: "Whale", Window: "12", At: now, Step: "away",
				Origin: signinbrowser.Origin("http://127.0.0.1:8123/start")}))
			runtime := signin.Runtime{Clock: func() time.Time { return now }, Run: func(_ context.Context, script string) (string, error) {
				if strings.Contains(script, "count of tabs") {
					return "1 " + tc.args.address, nil
				}
				if strings.Contains(script, "close window") {
					return tc.args.close, tc.args.failure
				}
				return "false " + tc.args.address, nil
			}}
			word, err := signin.New(signin.Config{Data: data, Session: "s1", Now: now}, runtime).Wait()
			require.NoError(t, err)
			_, found, err := files.Read("s1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, want{word, found})
		})
	}
}

func TestServiceOpenWriteFailure(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	path := filepath.Join(data, "signin", "s1.json")
	require.NoError(t, os.MkdirAll(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "blocked"), nil, 0o600))
	log := filepath.Join(t.TempDir(), "app.log")
	require.NoError(t, os.WriteFile(log, []byte("http://localhost:8123/start"), 0o600))
	var calls []bool
	runtime := signin.Runtime{Run: func(_ context.Context, script string) (string, error) {
		closing := strings.Contains(script, "close window")
		calls = append(calls, closing)
		if closing {
			return "gone", nil
		}
		return "12", nil
	}}
	word, err := signin.New(signin.Config{Data: data, Session: "s1", Now: time.Now()}, runtime).Open(log, 0, "Whale")
	assert.Error(t, err)
	assert.Empty(t, word)
	assert.Equal(t, []bool{false, true}, calls)
}

func TestServiceWaitRemoveFailure(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.NewSignins(data).Write("s1", store.Signin{Browser: "Whale", Window: "12", At: now}))
	runtime := signin.Runtime{Clock: func() time.Time { return now }, Run: func(context.Context, string) (string, error) {
		path := filepath.Join(data, "signin", "s1.json")
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Mkdir(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "blocked"), nil, 0o600))
		return "gone", nil
	}}
	word, err := signin.New(signin.Config{Data: data, Session: "s1", Now: now}, runtime).Wait()
	assert.Error(t, err)
	assert.Empty(t, word)
}
