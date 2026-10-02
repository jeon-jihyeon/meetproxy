// Package locmap stores evidence paths by request topic
package locmap

import (
	"os"
	"path/filepath"
	"strings"
)

// Worktree paths are normalized to the main checkout
type Path struct {
	Repo string `json:"repo"`
	Root string `json:"root"`
	Rel  string `json:"rel"`
}

func (p Path) Abs() string { return filepath.Join(p.Root, p.Rel) }

func (p Path) Exists() bool {
	_, err := os.Stat(p.Abs())
	return err == nil
}

func Resolve(raw string) (Path, bool) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return Path{}, false
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if root, ok := mainRoot(dir); ok {
			rel, err := filepath.Rel(dir, abs)
			if err != nil {
				return Path{}, false
			}
			return Path{Repo: filepath.Base(root), Root: root, Rel: rel}, true
		}
		if dir == filepath.Dir(dir) {
			return Path{}, false
		}
	}
}

// A worktree `.git` file points into the main repo's `.git/worktrees`
func mainRoot(dir string) (string, bool) {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return dir, true
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return "", false
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	sep := string(filepath.Separator)
	if root, _, ok := strings.Cut(filepath.Clean(gitdir), sep+".git"+sep+"worktrees"); ok {
		return root, true
	}
	return dir, true
}
