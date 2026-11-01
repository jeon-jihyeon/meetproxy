package osdialog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/osdialog/dialogtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only a hang waits out its limit, so every other row gets one a loaded machine never reaches
func dialogTestLimit(mode string) time.Duration {
	if mode == "hang" {
		return time.Second
	}
	return 10 * time.Second
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
			f := dialogtest.New(t, "osascript", dialogtest.Osascript)
			f.Set(tc.mode, " s3 cret ")
			answer, ok, err := NewOsa(f.Path, dialogTestLimit(tc.mode)).Input("slack_token", `Paste "it" \ here`)
			assert.Equal(t, tc.want, want{answer, ok, err != nil})
			assert.Equal(t, "\n", f.Read("argv"), "osascript gets no argument, so the script is read from stdin")
			script := f.Read("stdin")
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
			f := dialogtest.New(t, "zenity", dialogtest.Zenity)
			f.Set(tc.mode, "s3cret")
			env := []string{"PATH=/usr/bin:/bin", "ZENITY_OK=7", "DIALOG_CANCEL=0", "MEETPROXY_TEST_KEEP=fixture"}
			answer, ok, err := NewZenity(f.Path, env, dialogTestLimit(tc.mode)).Input("sample_login", "the password")
			assert.Equal(t, tc.want, want{answer, ok, err != nil})
			assert.Equal(t, "--password --modal --title=meetproxy sample_login\n", f.Read("argv"))
			assert.NotContains(t, f.Read("env"), "ZENITY_")
			assert.NotContains(t, f.Read("env"), "DIALOG_")
			assert.Contains(t, f.Read("env"), "kept")
		})
	}
}

func TestDialog_ShowPutsTheTextOnStdinAndKillEndsIt(t *testing.T) {
	t.Parallel()
	type showing interface {
		Show(string, string) (Shown, error)
		Kill(Shown)
	}
	tcs := []struct {
		name   string
		dialog func(path string) showing
		script string
		argv   string
	}{
		{
			"osascript", func(p string) showing { return NewOsa(p, 0) }, dialogtest.Osascript, "\n",
		},
		{
			"zenity", func(p string) showing { return NewZenity(p, os.Environ(), 0) }, dialogtest.Zenity,
			"--text-info --title=meetproxy sample_login --timeout=300\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := dialogtest.New(t, tc.name, tc.script)
			d := tc.dialog(f.Path)
			shown, err := d.Show("sample_login", "Type this code in sample app on your phone: 482913")
			pid := shown.Pid
			require.NoError(t, err)
			require.Eventually(t, func() bool { return len(f.Shown()) == 1 }, 5*time.Second, 10*time.Millisecond)
			assert.Equal(t, tc.argv, f.Read("argv"), "the passcode is in no argv")
			assert.Contains(t, f.Read("stdin"), "482913", "the passcode reaches the program on stdin")
			if tc.name == "osascript" {
				assert.Contains(t, f.Read("stdin"), `with title "meetproxy sample_login"`)
				assert.Contains(t, f.Read("stdin"), "giving up after 300")
				assert.True(t, strings.HasPrefix(f.Read("stdin"), "activate\n"), "the passcode dialog comes to the front")
			}
			pgid, err := syscall.Getpgid(pid)
			require.NoError(t, err)
			assert.Equal(t, pid, pgid, "the dialog leads its own process group")
			assert.NotEqual(t, syscall.Getpgrp(), pgid)
			d.Kill(shown)
			assert.True(t, dialogtest.Gone(t, pid), "Kill ends the dialog")
		})
	}
}

func TestNoDialog(t *testing.T) {
	t.Parallel()
	_, ok, err := Unavailable{}.Input("slack_token", "x")
	assert.False(t, ok)
	assert.ErrorIs(t, err, ErrUnavailable)
	_, err = Unavailable{}.Show("sample_login", "x")
	assert.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, "this computer can't show a sign-in dialog", ErrUnavailable.Error())
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
	assert.False(t, IsTerminal(null))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = r.Close(), w.Close() })
	assert.False(t, IsTerminal(w))
	file, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	assert.False(t, IsTerminal(file))
	assert.False(t, IsTerminal(&strings.Builder{}))
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

	Osa{path: filepath.Join(t.TempDir(), "osascript")}.Kill(Shown{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)})

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
	d := Osa{path: sleep}

	d.Kill(Shown{Pid: cmd.Process.Pid, Started: "Thu Jan  1 00:00:00 2026"})

	select {
	case err := <-exited:
		exited <- err
		t.Fatalf("a group started at another time was killed: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	d.Kill(Shown{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)})
	select {
	case err := <-exited:
		exited <- err
	case <-time.After(5 * time.Second):
		t.Fatal("the group of the dialog that was shown stayed")
	}
}
