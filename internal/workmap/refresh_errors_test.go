package workmap_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/workmap"
)

func TestRefreshErrorsRetry(t *testing.T) {
	type args struct {
		path, block string
		transcript  bool
		badGlob     bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"lock cannot open", args{path: "refresh.lock", block: "directory"}},
		{"state cannot read", args{path: "state.json", block: "directory"}},
		{"state cannot decode", args{path: "state.json", block: "corrupt"}},
		{"formats cannot read", args{path: "formats.json", block: "directory"}},
		{"formats cannot decode", args{path: "formats.json", block: "corrupt"}},
		{"transcript glob is invalid", args{badGlob: true}},
		{"transcript cannot stat", args{path: "b.jsonl", block: "loop", transcript: true}},
		{"transcript cannot read", args{path: "b.jsonl", block: "directory", transcript: true}},
		{"cases cannot read", args{path: "cases.jsonl", block: "directory"}},
		{"places cannot save", args{path: "places.json", block: "directory"}},
		{"methods cannot save", args{path: "methods.json", block: "directory"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newMachine(t)
			data := t.TempDir()
			s := workmap.New(data)
			at := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			transcript(t, m.config, "a", false, line(t, prompt(m.svc, "first neutral request", nil)))
			ran, err := s.Refresh(m.config, false, at)
			require.NoError(t, err)
			require.True(t, ran)
			transcript(t, m.config, "a", false, line(t, prompt(m.svc, "second neutral request", nil)))
			config := m.config
			repair := func() {}
			if tc.args.badGlob {
				config = filepath.Join(config, "[")
			}
			if tc.args.path != "" {
				base := filepath.Join(data, "workmap")
				if tc.args.transcript {
					base = filepath.Join(m.config, "projects", "-p")
				}
				repair = blockRefreshFile(t, filepath.Join(base, tc.args.path), tc.args.block)
			}
			state := filepath.Join(data, "workmap", "state.json")
			before, beforeErr := os.ReadFile(state)

			ran, err = s.Refresh(config, false, at.Add(time.Hour))

			assert.Error(t, err)
			assert.False(t, ran)
			after, afterErr := os.ReadFile(state)
			assert.Equal(t, before, after)
			assert.Equal(t, fmt.Sprint(beforeErr), fmt.Sprint(afterErr))
			repair()
			ran, err = s.Refresh(m.config, false, at.Add(time.Hour))
			require.NoError(t, err)
			assert.True(t, ran)
			cases, err := s.Cases()
			require.NoError(t, err)
			assert.Equal(t, []string{m.svc, m.svc}, roots(cases))
			written, err := os.ReadFile(state)
			require.NoError(t, err)
			var next struct {
				Offsets     map[string]int64 `json:"offsets"`
				RefreshedAt time.Time        `json:"refreshed_at"`
			}
			require.NoError(t, json.Unmarshal(written, &next))
			file := filepath.Join(m.config, "projects", "-p", "a.jsonl")
			info, err := os.Stat(file)
			require.NoError(t, err)
			assert.Equal(t, info.Size(), next.Offsets[file])
			assert.Equal(t, at.Add(time.Hour), next.RefreshedAt)
			ran, err = s.Refresh(m.config, true, at.Add(time.Hour))
			require.NoError(t, err)
			assert.False(t, ran)
		})
	}
}

func blockRefreshFile(t *testing.T, path, kind string) func() {
	t.Helper()
	before, err := os.ReadFile(path)
	existed := !os.IsNotExist(err)
	if existed {
		require.NoError(t, err)
		require.NoError(t, os.Remove(path))
	}
	switch kind {
	case "directory":
		require.NoError(t, os.Mkdir(path, 0700))
	case "corrupt":
		require.NoError(t, os.WriteFile(path, []byte("not JSON"), 0600))
	case "loop":
		require.NoError(t, os.Symlink(filepath.Base(path), path))
	default:
		t.Fatalf("unknown fixture kind %q", kind)
	}
	return func() {
		require.NoError(t, os.RemoveAll(path))
		if existed {
			require.NoError(t, os.WriteFile(path, before, 0600))
		}
	}
}
