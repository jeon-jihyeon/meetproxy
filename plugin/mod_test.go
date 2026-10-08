package plugin_test

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Runs the watcher tests with node against a freshly built binary
// Each node test is reported as a subtest under its own name
func TestWatcher(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	readSources(t)
	bin := filepath.Join(t.TempDir(), "meetproxy")
	build := exec.Command("go", "build", "-o", bin, "../cmd/meetproxy")
	build.Stderr = os.Stderr
	require.NoError(t, build.Run())

	cmd := exec.Command(node, "--test", "--test-reporter=tap", "hooks/meetproxy.test.mjs")
	cmd.Env = append(os.Environ(), "MEETPROXY_BIN="+bin)
	out, runErr := cmd.CombinedOutput()
	results := tapResults(out)
	require.NotEmpty(t, results, string(out))
	failed := false
	for _, r := range results {
		failed = failed || r.failed
		t.Run(r.name, func(t *testing.T) {
			if r.skip != "" {
				t.Skip(r.skip)
			}
			if r.failed {
				t.Error(r.detail)
			}
		})
	}
	if !failed {
		require.NoError(t, runErr, string(out))
	}
}

type tapResult struct {
	name   string
	failed bool
	// SKIP or TODO when node did not run the test
	skip string
	// The lines node printed after the result such as the error and its stack
	detail string
}

// A top level TAP result line of node --test
var tapLine = regexp.MustCompile(`^(not )?ok \d+ - (.*?)(?: # (SKIP|TODO).*)?$`)

func tapResults(out []byte) []tapResult {
	var results []tapResult
	var detail []string
	flush := func() {
		if len(results) > 0 {
			results[len(results)-1].detail = strings.Join(detail, "\n")
		}
		detail = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		m := tapLine.FindStringSubmatch(sc.Text())
		if m == nil {
			detail = append(detail, sc.Text())
			continue
		}
		flush()
		results = append(results, tapResult{name: m[2], failed: m[1] != "", skip: m[3]})
	}
	flush()
	return results
}

// go test reruns only when a file the test opened changes
// The test opens every source the binary is built from and the watcher files so a change to any reruns it
func readSources(t *testing.T) {
	t.Helper()
	for _, dir := range []string{"../cmd", "../internal"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) != ".go" {
				return err
			}
			_, err = os.ReadFile(path)
			return err
		})
		require.NoError(t, err)
	}
	watcher := []string{
		"hooks/meetproxy.js", "hooks/core.js", "hooks/slack.js", "hooks/github.js", "hooks/meetproxy.test.mjs",
		"hooks/testdata/links.json",
	}
	for _, f := range watcher {
		_, err := os.ReadFile(f)
		require.NoError(t, err)
	}
}
