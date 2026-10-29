package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fake dialog program in its own directory
// 1. Each run appends its argv to argv, its stdin to stdin and its environment to env
// 2. An input run answers as the mode file says with the secret file's text, so no secret is in its argv
// 3. A show run records its pid in shown and waits until it is killed, still running as itself as a real dialog does
type fakeDialog struct {
	t    *testing.T
	dir  string
	path string
}

const fakeOsascript = `#!/bin/sh
d=${0%/*}
printf '%s\n' "$*" >> "$d/argv"
env >> "$d/env"
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
const fakeZenity = `#!/bin/sh
d=${0%/*}
printf '%s\n' "$*" >> "$d/argv"
env >> "$d/env"
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

func newFakeDialog(t *testing.T, name, script string) *fakeDialog {
	t.Helper()
	dir := t.TempDir()
	f := &fakeDialog{t: t, dir: dir, path: filepath.Join(dir, name)}
	require.NoError(t, os.WriteFile(f.path, []byte(script), 0o755))
	f.set("ok", "s3cret")
	return f
}

func (f *fakeDialog) set(mode, secret string) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, "mode"), []byte(mode), 0o600))
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, "secret"), []byte(secret), 0o600))
}

func (f *fakeDialog) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	require.NoError(f.t, err)
	return string(b)
}

// Only a hang waits out its limit, so every other row gets one a loaded machine never reaches
func dialogTestLimit(mode string) time.Duration {
	if mode == "hang" {
		return time.Second
	}
	return 10 * time.Second
}

// The pids of the show runs
func (f *fakeDialog) shown() []string { return strings.Fields(f.read("shown")) }

// Waits until the process of pid is gone and reaped
func gone(t *testing.T, pid int) bool {
	t.Helper()
	for range 100 {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestOsaDialog_Input(t *testing.T) {
	t.Parallel()
	type want struct {
		answer string
		ok     bool
		failed bool
	}
	tcs := []struct {
		name string
		mode string
		want want
	}{
		{"ok keeps the spaces of the answer and cuts one newline", "ok", want{" s3 cret ", true, false}},
		{"a dialog that gave up is no answer", "gaveup", want{"", false, false}},
		{"Cancel is no answer", "cancel", want{"", false, false}},
		{"another error fails", "fail", want{"", false, true}},
		{"a run past its limit gave up", "hang", want{"", false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeDialog(t, "osascript", fakeOsascript)
			f.set(tc.mode, " s3 cret ")
			answer, ok, err := osaDialog{path: f.path, limit: dialogTestLimit(tc.mode)}.Input("slack_token", `Paste "it" \ here`)
			assert.Equal(t, tc.want, want{answer, ok, err != nil})
			assert.Equal(t, "\n", f.read("argv"), "osascript gets no argument, so the script is read from stdin")
			script := f.read("stdin")
			assert.Contains(t, script, `with title "meetproxy slack_token"`)
			assert.Contains(t, script, `display dialog "Paste \"it\" \\ here"`)
			assert.Contains(t, script, `default answer "" with hidden answer giving up after 60`)
			assert.Contains(t, script, "if gave up of r then")
			assert.NotContains(t, script, "tell ", "a tell block would send an Apple event")
			assert.True(t, strings.HasPrefix(script, "activate\n"), "a paste goes into the dialog, never the window behind it")
			if err != nil {
				assert.NotContains(t, err.Error(), "syntax error", "no stderr text reaches an error")
			}
		})
	}
}

func TestParseDialogInput(t *testing.T) {
	t.Parallel()
	type answer struct {
		text string
		end  dialogEnd
	}
	osa := []struct {
		name string
		run  dialogRun
		want answer
	}{
		{"an answer", dialogRun{stdout: []byte("answer:a b\n")}, answer{"a b", dialogOK}},
		{"only one newline is cut", dialogRun{stdout: []byte("answer:x\n\n")}, answer{"x\n", dialogOK}},
		{"an empty answer", dialogRun{stdout: []byte("answer:\n")}, answer{"", dialogOK}},
		{"gave up", dialogRun{stdout: []byte("gave up\n")}, answer{"", dialogGaveUp}},
		{"the text gave up typed as the answer", dialogRun{stdout: []byte("answer:gave up\n")}, answer{"gave up", dialogOK}},
		{"Cancel", dialogRun{code: 1, stderr: []byte("0:17: execution error: User canceled. (-128)\n")}, answer{"", dialogCancel}},
		{"another error with exit 1", dialogRun{code: 1, stderr: []byte("0:17: execution error: x (-1713)\n")}, answer{"", dialogBroken}},
		{"-128 with another exit", dialogRun{code: 2, stderr: []byte("(-128)\n")}, answer{"", dialogBroken}},
		{"unmarked output", dialogRun{stdout: []byte("s3cret\n")}, answer{"", dialogBroken}},
		{"timed out", dialogRun{stdout: []byte("answer:x\n"), timedOut: true}, answer{"", dialogGaveUp}},
	}
	for _, tc := range osa {
		text, end := parseOsaInput(tc.run)
		assert.Equal(t, tc.want, answer{text, end}, "osascript: "+tc.name)
	}
	zenity := []struct {
		name string
		run  dialogRun
		want answer
	}{
		{"an answer", dialogRun{stdout: []byte(" pw \n")}, answer{" pw ", dialogOK}},
		{"Cancel or Esc", dialogRun{code: 1}, answer{"", dialogCancel}},
		{"a timeout code with the text typed so far", dialogRun{code: 5, stdout: []byte("half\n")}, answer{"", dialogBroken}},
		{"timed out in this process", dialogRun{stdout: []byte("half\n"), timedOut: true}, answer{"", dialogGaveUp}},
		{"another code", dialogRun{code: 255, stdout: []byte("x\n")}, answer{"", dialogBroken}},
	}
	for _, tc := range zenity {
		text, end := parseZenityInput(tc.run)
		assert.Equal(t, tc.want, answer{text, end}, "zenity: "+tc.name)
	}
}

func TestZenityDialog_Input(t *testing.T) {
	t.Parallel()
	type want struct {
		answer string
		ok     bool
		failed bool
	}
	tcs := []struct {
		name string
		mode string
		want want
	}{
		{"ok", "ok", want{"s3cret", true, false}},
		{"Cancel", "cancel", want{"", false, false}},
		{"the text a timeout printed is dropped", "timeout", want{"", false, true}},
		{"the limit of this process gives up", "hang", want{"", false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeDialog(t, "zenity", fakeZenity)
			f.set(tc.mode, "s3cret")
			env := dialogEnv([]string{"PATH=/usr/bin:/bin", "ZENITY_OK=7", "DIALOG_CANCEL=0", "HOME=/home/ann"})
			answer, ok, err := zenityDialog{path: f.path, env: env, limit: dialogTestLimit(tc.mode)}.Input("sample_login", "the password")
			assert.Equal(t, tc.want, want{answer, ok, err != nil})
			assert.Equal(t, "--password --modal --title=meetproxy sample_login\n", f.read("argv"))
			assert.NotContains(t, f.read("env"), "ZENITY_")
			assert.NotContains(t, f.read("env"), "DIALOG_")
		})
	}
}

func TestDialog_ShowPutsTheTextOnStdinAndKillEndsIt(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name   string
		dialog func(path string) osDialog
		script string
		argv   string
	}{
		{
			"osascript", func(p string) osDialog { return osaDialog{path: p} }, fakeOsascript, "\n",
		},
		{
			"zenity", func(p string) osDialog { return zenityDialog{path: p, env: dialogEnv(os.Environ())} }, fakeZenity,
			"--text-info --title=meetproxy sample_login --timeout=300\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeDialog(t, tc.name, tc.script)
			d := tc.dialog(f.path)
			shown, err := d.Show("sample_login", "Type this code in sample app on your phone: 482913")
			pid := shown.Pid
			require.NoError(t, err)
			require.Eventually(t, func() bool { return len(f.shown()) == 1 }, 5*time.Second, 10*time.Millisecond)
			assert.Equal(t, tc.argv, f.read("argv"), "the passcode is in no argv")
			assert.Contains(t, f.read("stdin"), "482913", "the passcode reaches the program on stdin")
			if tc.name == "osascript" {
				assert.Contains(t, f.read("stdin"), `with title "meetproxy sample_login"`)
				assert.Contains(t, f.read("stdin"), "giving up after 300")
				assert.True(t, strings.HasPrefix(f.read("stdin"), "activate\n"), "the passcode dialog comes to the front")
			}
			pgid, err := syscall.Getpgid(pid)
			require.NoError(t, err)
			assert.Equal(t, pid, pgid, "the dialog leads its own process group")
			assert.NotEqual(t, syscall.Getpgrp(), pgid)
			d.Kill(shown)
			assert.True(t, gone(t, pid), "Kill ends the dialog")
		})
	}
}

func TestNoDialog(t *testing.T) {
	t.Parallel()
	_, ok, err := noDialog{}.Input("slack_token", "x")
	assert.False(t, ok)
	assert.ErrorIs(t, err, errNoDialog)
	_, err = noDialog{}.Show("sample_login", "x")
	assert.ErrorIs(t, err, errNoDialog)
	assert.Equal(t, "this computer can't show a sign-in dialog", errNoDialog.Error())
}

func TestDialogEnv(t *testing.T) {
	t.Parallel()
	got := dialogEnv([]string{"PATH=/bin", "ZENITY_TIMEOUT=0", "DIALOG_OK=1", "DISPLAY=:0", "MY_ZENITY_X=1"})
	assert.Equal(t, []string{"PATH=/bin", "DISPLAY=:0", "MY_ZENITY_X=1"}, got)
}

// A mode check calls /dev/null a terminal, the termios check doesn't
func TestIsTerminal(t *testing.T) {
	t.Parallel()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = null.Close() })
	info, err := null.Stat()
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeCharDevice, "/dev/null is a character device")
	assert.False(t, isTerminal(null))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = r.Close(), w.Close() })
	assert.False(t, isTerminal(w))
	file, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	assert.False(t, isTerminal(file))
	assert.False(t, isTerminal(&strings.Builder{}))
}

// A pid whose dialog ended may lead another group by now, which Kill never signals
// The other program is reaped as it exits so a killed one never passes as a running zombie
func TestOsaDialog_KillLeavesAnotherProgramsGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	newGroup(cmd)
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })

	osaDialog{path: filepath.Join(t.TempDir(), "osascript")}.Kill(shownDialog{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)})

	select {
	case err := <-exited:
		exited <- err
		t.Fatalf("the other program's group was killed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

// A pid given again to another group of the same program started at another time, so Kill never signals it
func TestOsaDialog_KillLeavesAGroupStartedAtAnotherTime(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	cmd := exec.Command(sleep, "30")
	newGroup(cmd)
	require.NoError(t, cmd.Start())
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	d := osaDialog{path: sleep}

	d.Kill(shownDialog{Pid: cmd.Process.Pid, Started: "Thu Jan  1 00:00:00 2026"})

	select {
	case err := <-exited:
		exited <- err
		t.Fatalf("a group started at another time was killed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	d.Kill(shownDialog{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)})
	select {
	case err := <-exited:
		exited <- err
	case <-time.After(5 * time.Second):
		t.Fatal("the group of the dialog that was shown stayed")
	}
}
