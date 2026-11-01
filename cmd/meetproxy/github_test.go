package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A gh that answers each API call from the file named after its method and path in $FAKE_GH_DIR
// A paginated read wraps the file in one page and an unknown path fails as gh does
const fakeGHScript = `#!/bin/sh
method=GET
path=
slurp=
shift
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method=$2; shift ;;
    -f|-q|--input) shift ;;
    --slurp) slurp=1 ;;
    --paginate) ;;
    *) [ -z "$path" ] && path=$1 ;;
  esac
  shift
done
echo "$method $path" >> "$FAKE_GH_DIR/calls"
if [ "$path" = user ]; then echo me; exit 0; fi
file="$FAKE_GH_DIR/$method $(echo "$path" | tr '/' '_')"
if [ ! -f "$file" ]; then echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
[ "$method" = GET ] || cat > "$FAKE_GH_DIR/stdin"
[ -n "$slurp" ] && printf '['
cat "$file"
[ -n "$slurp" ] && printf ']'
exit 0
`

// Puts the fake gh first on PATH with the answers keyed by method and API path
func fakeGH(t *testing.T, answers map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(fakeGHScript), 0o755))
	for key, body := range answers {
		require.NoError(t, os.WriteFile(filepath.Join(dir, strings.ReplaceAll(key, "/", "_")), []byte(body), 0o600))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_DIR", dir)
	return dir
}

type githubWant struct {
	code   int
	out    string
	failed bool
}

// Not parallel since the fake gh is found through the environment
// The steps run in order on one data directory as a session would
func TestRun_GitHub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	issue := "https://github.com/o/r/issues/3"
	mention := issue + "#issuecomment-11"
	_ = fakeGH(t, map[string]string{
		"GET repos/o/r/issues/3/comments": `[{"id":11,"user":{"login":"kai","type":"User"},"body":"@me why",` +
			`"created_at":"2030-01-01T00:01:40Z","html_url":"` + mention + `","author_association":"MEMBER"},` +
			`{"id":12,"user":{"login":"me","type":"User"},"body":"because","created_at":"2030-01-01T00:03:20Z",` +
			`"html_url":"` + issue + `#issuecomment-12"}]`,
		"GET repos/o/r/issues/3": `{"title":"Broken","body":"it fails","user":{"login":"kai"}}`,
	})
	now := time.Unix(1893456400, 0)
	data := t.TempDir()
	steps := []struct {
		name string
		run  step
		want githubWant
	}{
		{"whoami asks gh", step{"github", []string{"whoami"}, ""}, githubWant{0, `{"user":"me"}`, false}},
		{"read joins the issue and its comments", step{"github", []string{"read", issue, "--limit", "2"}, ""}, githubWant{0, `{"text":"kai: Broken\nit fails\nkai: @me why"}`, false}},
		{"usage error for a reaction GitHub does not have", step{"github", []string{"react", issue, "--react", "white_check_mark"}, ""}, githubWant{exitUsage, "", true}},
		{"usage error for a Slack link", step{"github", []string{"read", "https://acme-alt.slack.com/archives/C1/p1893456100000001"}, ""}, githubWant{exitUsage, "", true}},
		{"usage error for a post without text", step{"github", []string{"post", mention}, " "}, githubWant{exitUsage, "", true}},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			var out bytes.Buffer
			code, err := run(s.run.cmd, append([]string{"--data", data}, s.run.args...), "s1", now, strings.NewReader(s.run.stdin), &out)
			got := strings.TrimSpace(out.String())
			assert.Equal(t, s.want.code, code)
			assert.Equal(t, s.want.failed, err != nil, err)
			if strings.HasPrefix(s.want.out, `"`) {
				assert.Contains(t, got, s.want.out)
				return
			}
			assert.Equal(t, s.want.out, got)
		})
	}

}
