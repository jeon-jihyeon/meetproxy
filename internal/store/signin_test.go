package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignins(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name, session string
		invalid       bool
	}{
		{"valid", "s-1", false},
		{"empty", "", true},
		{"parent", "../outside", true},
		{"nested", "s/other", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			files := store.NewSignins(data)
			row := store.Signin{Browser: "Whale", Window: "12", At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
				Step: "away", Printed: "fingerprint", Origin: "origin"}
			err := files.Write(tc.session, row)
			assert.Equal(t, tc.invalid, err != nil)
			got, found, err := files.Read(tc.session)
			assert.Equal(t, tc.invalid, err != nil)
			assert.Equal(t, !tc.invalid, found)
			rows := []store.Signin{row, {}}
			index := 0
			if tc.invalid {
				index = 1
			}
			assert.Equal(t, rows[index], got)
			assert.Equal(t, tc.invalid, files.Remove(tc.session) != nil)
			_, found, err = files.Read(tc.session)
			assert.Equal(t, tc.invalid, err != nil)
			assert.False(t, found)
			assert.NoFileExists(t, filepath.Join(data, "outside.json"))
		})
	}
}

func TestSigninsWire(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	files := store.NewSignins(data)
	row := store.Signin{Browser: "Whale", Window: "12", At: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	require.NoError(t, files.Write("s1", row))
	path := filepath.Join(data, "signin", "s1.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	var keys map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &keys))
	assert.Len(t, keys, 3)
	assert.JSONEq(t, `{"browser":"Whale","window":"12","at":"2026-01-02T00:00:00Z"}`, string(body))
	require.NoError(t, files.Remove("s1"))
	require.NoError(t, files.Remove("s1"))
}
