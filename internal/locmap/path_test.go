package locmap_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/locmap"
)

func TestResolve(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	svc := filepath.Join(base, "svc")
	wt := filepath.Join(base, "svc-wt-1")
	relWt := filepath.Join(base, "svc-wt-2")
	for _, d := range []string{
		filepath.Join(svc, ".git", "worktrees", "svc-wt-1"),
		filepath.Join(svc, ".git", "worktrees", "svc-wt-2"),
		filepath.Join(svc, "pkg"), filepath.Join(wt, "pkg"), filepath.Join(relWt, "pkg"),
	} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	absGitdir := "gitdir: " + filepath.Join(svc, ".git", "worktrees", "svc-wt-1") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte(absGitdir), 0o644))
	relGitdir := "gitdir: ../svc/.git/worktrees/svc-wt-2\n"
	require.NoError(t, os.WriteFile(filepath.Join(relWt, ".git"), []byte(relGitdir), 0o644))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	relInput, err := filepath.Rel(cwd, filepath.Join(svc, "pkg", "a.go"))
	require.NoError(t, err)
	rel := filepath.Join("pkg", "a.go")

	type want struct {
		ok   bool
		name string
		root string
		rel  string
	}
	tcs := []struct {
		name string
		raw  string
		want want
	}{
		{"main checkout", filepath.Join(svc, "pkg", "a.go"), want{true, "svc", svc, rel}},
		{"worktree maps to main", filepath.Join(wt, "pkg", "a.go"), want{true, "svc", svc, rel}},
		{"worktree with a relative gitdir", filepath.Join(relWt, "pkg", "a.go"), want{true, "svc", svc, rel}},
		{"relative input becomes absolute", relInput, want{true, "svc", svc, rel}},
		{"outside a repository", filepath.Join(base, "plain", "a.go"), want{false, "", "", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, ok := locmap.Resolve(tc.raw)
			assert.Equal(t, tc.want, want{ok, p.Name, p.Root, p.Rel})
		})
	}
}

func TestResolveIn(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	svc := filepath.Join(base, "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, ".git"), 0o755))
	notes := filepath.Join(base, "notes")
	type want struct {
		ok   bool
		name string
		rel  string
	}
	tcs := []struct {
		name string
		raw  string
		dir  string
		want want
	}{
		{"a repository wins over the session directory", filepath.Join(svc, "a.go"), notes, want{true, "svc", "a.go"}},
		{"a file of the session directory belongs to it", filepath.Join(notes, "oncall", "runbook.md"), notes, want{true, "notes", filepath.Join("oncall", "runbook.md")}},
		{"a file elsewhere belongs to nothing", filepath.Join(base, "other", "x.md"), notes, want{false, "", ""}},
		{"no session directory means repositories only", filepath.Join(notes, "x.md"), "", want{false, "", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, ok := locmap.ResolveIn(tc.raw, tc.dir)
			assert.Equal(t, tc.want, want{ok, p.Name, p.Rel})
		})
	}
}

func TestPlace(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	svc := filepath.Join(base, "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, ".git"), 0o755))
	notes := filepath.Join(base, "notes")
	type want struct {
		root string
		name string
	}
	tcs := []struct {
		name string
		dir  string
		want want
	}{
		{"a directory in a repository is the repository", filepath.Join(svc, "pkg"), want{svc, "svc"}},
		{"a directory outside any repository is itself", notes, want{notes, "notes"}},
		{"a trailing separator is ignored", notes + string(filepath.Separator), want{notes, "notes"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, name := locmap.Place(tc.dir)
			assert.Equal(t, tc.want, want{root, name})
		})
	}
}
