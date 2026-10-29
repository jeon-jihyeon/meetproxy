package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	osascriptPath = "/usr/bin/osascript"
	// An input dialog gives up after this
	inputGivesUp = 60 * time.Second
	// A passcode dialog gives up after this so a kill that never comes still closes it
	showGivesUp = 5 * time.Minute
	// osascript gives up by itself, so its run is killed only this long after that
	dialogGrace = 5 * time.Second
	// Bounds the wait for the pipes once a dialog was killed
	dialogWaitDelay = 2 * time.Second
	// Marks the answer osascript prints so a gave up dialog never reads as an empty answer
	answerMark = "answer:"
	gaveUpMark = "gave up"
)

// How an input dialog ended
type dialogEnd int

const (
	dialogOK     dialogEnd = iota // the user typed an answer and pressed OK
	dialogCancel                  // the user pressed Cancel or Esc
	dialogGaveUp                  // nobody answered in time
	dialogBroken                  // the program failed or answered what no dialog answers
)

var errDialogFailed = errors.New("the dialog program failed")

// The dialog program of this computer, noDialog when it has none
// Only main calls it so a test never opens a real dialog
func systemDialog() osDialog {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(osascriptPath); err == nil {
			return osaDialog{path: osascriptPath}
		}
	case "linux":
		if p, err := exec.LookPath("zenity"); err == nil {
			return zenityDialog{path: p, env: dialogEnv(os.Environ())}
		}
	}
	return noDialog{}
}

func dialogTitle(tool string) string { return "meetproxy " + tool }

// What one run of an input dialog program gave
type dialogRun struct {
	stdout []byte
	stderr []byte
	code   int
	// The run outlived its limit and was killed
	timedOut bool
}

// Runs a dialog program to its end with the script on stdin
// 1. Nothing secret goes in args, only the program and fixed flags
// 2. Its own process group is killed at the limit so no child it started outlives it
func runDialog(path string, args, env []string, stdin string, limit time.Duration) (dialogRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = dialogWaitDelay
	ownGroup(cmd)
	err := cmd.Run()
	r := dialogRun{stdout: stdout.Bytes(), stderr: stderr.Bytes(), timedOut: err != nil && ctx.Err() != nil}
	var exit *exec.ExitError
	switch {
	case err == nil, r.timedOut:
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	default:
		return dialogRun{}, errDialogFailed
	}
	return r, nil
}

// Starts a dialog program that outlives this process
// The text goes on stdin and the pipe closes at once, so neither argv nor an open pipe holds it
func startDialog(path string, args, env []string, stdin string) (shownDialog, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	newGroup(cmd)
	w, err := cmd.StdinPipe()
	if err != nil {
		return shownDialog{}, errDialogFailed
	}
	if err := cmd.Start(); err != nil {
		return shownDialog{}, errDialogFailed
	}
	shown := shownDialog{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)}
	_, werr := io.WriteString(w, stdin)
	if err := errors.Join(werr, w.Close()); err != nil {
		killGroup(shown, path)
		_ = cmd.Wait()
		return shownDialog{}, errDialogFailed
	}
	// Reaps it when it ends while this process still runs
	go func() { _ = cmd.Wait() }()
	return shown, nil
}

// The environment without the ZENITY_ and DIALOG_ variables that change zenity's exit codes
func dialogEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "ZENITY_") && !strings.HasPrefix(kv, "DIALOG_") {
			out = append(out, kv)
		}
	}
	return out
}

// osascript with every script on stdin
// The scripts hold no tell block so they send no Apple event and ask for no Automation consent
// Each starts with a bare activate that brings osascript itself to the front, so a paste goes into the dialog and not into the window behind it
type osaDialog struct {
	path string
	// Zero kills an input run dialogGrace after it gave up
	limit time.Duration
}

func (d osaDialog) Input(tool, prompt string) (string, bool, error) {
	script := osaActivate + "set r to display dialog " + appleString(prompt) + " with title " + appleString(dialogTitle(tool)) +
		` default answer "" with hidden answer giving up after ` + seconds(inputGivesUp) + "\n" +
		`if gave up of r then return "` + gaveUpMark + `"` + "\n" +
		`return "` + answerMark + `" & text returned of r` + "\n"
	r, err := runDialog(d.path, nil, nil, script, cmp.Or(d.limit, inputGivesUp+dialogGrace))
	if err != nil {
		return "", false, err
	}
	return dialogAnswer(parseOsaInput(r))
}

func (d osaDialog) Show(tool, text string) (shownDialog, error) {
	script := osaActivate + "display dialog " + appleString(text) + " with title " + appleString(dialogTitle(tool)) +
		` buttons {"OK"} default button "OK" giving up after ` + seconds(showGivesUp) + "\n"
	return startDialog(d.path, nil, nil, script)
}

func (d osaDialog) Kill(shown shownDialog) { killGroup(shown, d.path) }

const osaActivate = "activate\n"

// 1. osascript prints the returned text and one newline, and only that newline is cut so spaces of a secret stay
// 2. Cancel is error -128 on stderr with exit 1, whatever position the line names
func parseOsaInput(r dialogRun) (string, dialogEnd) {
	switch {
	case r.timedOut:
		return "", dialogGaveUp
	case r.code == 0:
		s := strings.TrimSuffix(string(r.stdout), "\n")
		if s == gaveUpMark {
			return "", dialogGaveUp
		}
		if answer, ok := strings.CutPrefix(s, answerMark); ok {
			return answer, dialogOK
		}
	case r.code == 1 && strings.HasSuffix(strings.TrimRight(string(r.stderr), "\n"), "(-128)"):
		return "", dialogCancel
	}
	return "", dialogBroken
}

// zenity, judged by its exit code and by this process's clock
// zenity 3 answers its own timeout with the Cancel code, so no --timeout is given and the limit runs on this clock
type zenityDialog struct {
	path string
	env  []string
	// Zero is inputGivesUp
	limit time.Duration
}

// zenity's password dialog takes no prompt, so only the title names what it asks for
func (d zenityDialog) Input(tool, _ string) (string, bool, error) {
	r, err := runDialog(d.path, []string{"--password", "--modal", "--title=" + dialogTitle(tool)}, d.env, "", cmp.Or(d.limit, inputGivesUp))
	if err != nil {
		return "", false, err
	}
	return dialogAnswer(parseZenityInput(r))
}

// --text-info reads the text from stdin, unlike --info which takes it only in argv
func (d zenityDialog) Show(tool, text string) (shownDialog, error) {
	args := []string{"--text-info", "--title=" + dialogTitle(tool), "--timeout=" + seconds(showGivesUp)}
	return startDialog(d.path, args, d.env, text)
}

func (d zenityDialog) Kill(shown shownDialog) { killGroup(shown, d.path) }

// stdout counts only on exit 0, since a timed out entry still prints what was typed
func parseZenityInput(r dialogRun) (string, dialogEnd) {
	switch {
	case r.timedOut:
		return "", dialogGaveUp
	case r.code == 0:
		return strings.TrimSuffix(string(r.stdout), "\n"), dialogOK
	case r.code == 1:
		return "", dialogCancel
	}
	return "", dialogBroken
}

func dialogAnswer(answer string, end dialogEnd) (string, bool, error) {
	switch end {
	case dialogOK:
		return answer, true, nil
	case dialogBroken:
		return "", false, errDialogFailed
	}
	return "", false, nil
}

// An AppleScript string literal
// Control characters become spaces so the literal stays on one line
func appleString(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func seconds(d time.Duration) string { return strconv.Itoa(int(d / time.Second)) }
