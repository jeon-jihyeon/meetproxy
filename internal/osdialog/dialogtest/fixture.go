package dialogtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type Fixture struct {
	t    *testing.T
	Dir  string
	Path string
}

const Osascript = `#!/bin/sh
d=${0%/*}
printf '%s\n' "$*" >> "$d/argv"
in=$(cat)
printf '%s\n' "$in" >> "$d/stdin"
case "$in" in
*"hidden answer"*|"")
	case $(cat "$d/mode") in
	ok) printf 'answer:%s\n' "$(cat "$d/secret")" ;;
	gaveup) echo "gave up" ;;
	cancel) echo "0:121: execution error: User canceled. (-128)" >&2; exit 1 ;;
	fail) echo "0:9: syntax error" >&2; exit 1 ;;
	hang) exec sleep 30 ;;
	esac ;;
*) echo $$ >> "$d/shown"; sleep 300 ;;
esac
`

// zenity reads no stdin for --password, so the mode decides by its first flag
const Zenity = `#!/bin/sh
d=${0%/*}
printf '%s\n' "$*" >> "$d/argv"
if [ "${ZENITY_OK+x}" = x ]; then printf 'ZENITY_OK\n' >> "$d/env"; fi
if [ "${DIALOG_CANCEL+x}" = x ]; then printf 'DIALOG_CANCEL\n' >> "$d/env"; fi
if [ "${MEETPROXY_TEST_KEEP+x}" = x ]; then printf 'kept\n' >> "$d/env"; fi
if [ "$1" = "--text-info" ]; then
	cat >> "$d/stdin"
	echo $$ >> "$d/shown"
	sleep 300
fi
case $(cat "$d/mode") in
ok) printf '%s\n' "$(cat "$d/secret")" ;;
cancel) exit 1 ;;
timeout) printf 'half typed\n'; exit 5 ;;
hang) exec sleep 30 ;;
esac
`

func New(t *testing.T, name, script string) *Fixture {
	t.Helper()
	dir := t.TempDir()
	f := &Fixture{t: t, Dir: dir, Path: filepath.Join(dir, name)}
	if err := os.WriteFile(f.Path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Set("ok", "s3cret")
	return f
}

func (f *Fixture) Set(mode, secret string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, "mode"), []byte(mode), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, "secret"), []byte(secret), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *Fixture) Read(name string) string {
	f.t.Helper()
	body, err := os.ReadFile(filepath.Join(f.Dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return string(body)
}

func (f *Fixture) Shown() []string { return strings.Fields(f.Read("shown")) }
