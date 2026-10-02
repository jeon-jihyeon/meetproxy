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
		repo string
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
			assert.Equal(t, tc.want, want{ok, p.Repo, p.Root, p.Rel})
		})
	}
}
