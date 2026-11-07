package handoff_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/handoff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != "handoff-input-fixture" {
		return
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil || os.WriteFile(os.Args[2], body, 0o600) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSendInputBoundary(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	require.NoError(t, err)
	type want struct {
		err        error
		dispatched bool
	}
	tcs := []struct {
		name string
		body string
		want want
	}{
		{"at encoded limit", `"` + strings.Repeat("x", (4<<20)-2) + `"`, want{dispatched: true}},
		{"over encoded limit", `"` + strings.Repeat("x", (4<<20)-1) + `"`, want{err: handoff.ErrCommand}},
		{"invalid JSON", `broken`, want{err: handoff.ErrCommand}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			marker := filepath.Join(t.TempDir(), "received.json")
			command, err := handoff.New(binary, []string{"handoff-input-fixture", marker}, nil, 0)
			require.NoError(t, err)

			err = command.Send(context.Background(), []byte(tc.body))

			assert.ErrorIs(t, err, tc.want.err)
			body, readErr := os.ReadFile(marker)
			assert.Equal(t, !tc.want.dispatched, errors.Is(readErr, os.ErrNotExist))
			if tc.want.dispatched {
				require.NoError(t, readErr)
				assert.Equal(t, tc.body, string(body))
				assert.True(t, json.Valid(body))
			}
		})
	}
}
