package osdialog

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
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

type dialogEnd int

const (
	dialogOK     dialogEnd = iota // the user typed an answer and pressed OK
	dialogCancel                  // the user pressed Cancel or Esc
	dialogGaveUp                  // nobody answered in time
	dialogBroken                  // the program failed or answered what no dialog answers
)

var errFailed = errors.New("the dialog program failed")

func dialogTitle(tool string) string { return "meetproxy " + tool }

type dialogRun struct {
	stdout []byte
	stderr []byte
	code   int
	// The run outlived its limit and was killed
	timedOut bool
}

// 1. Args hold only the program and fixed flags so no secret reaches the process list
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
		return dialogRun{}, errFailed
	}
	return r, nil
}

// Starts a dialog program that outlives this process
// The text goes on stdin and the pipe closes at once, so neither argv nor an open pipe holds it
func startDialog(path string, args, env []string, stdin string) (Shown, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	newGroup(cmd)
	w, err := cmd.StdinPipe()
	if err != nil {
		return Shown{}, errFailed
	}
	if err := cmd.Start(); err != nil {
		return Shown{}, errFailed
	}
	shown := Shown{Pid: cmd.Process.Pid, Started: startedAt(cmd.Process.Pid)}
	_, werr := io.WriteString(w, stdin)
	if err := errors.Join(werr, w.Close()); err != nil {
		killGroup(shown, path)
		_ = cmd.Wait()
		return Shown{}, errFailed
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

// The scripts hold no tell block so they send no Apple event and ask for no Automation consent
// A bare activate brings osascript itself to the front so a paste never enters the window behind it
type Osa struct {
	path string
	// Zero kills an input run dialogGrace after it gave up
	limit time.Duration
}

func NewOsa(path string, limit time.Duration) Osa {
	return Osa{path: path, limit: limit}
}

func (d Osa) Input(tool, prompt string) (string, bool, error) {
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

func (d Osa) Show(tool, text string) (Shown, error) {
	script := osaActivate + "display dialog " + appleString(text) + " with title " + appleString(dialogTitle(tool)) +
		` buttons {"OK"} default button "OK" giving up after ` + seconds(showGivesUp) + "\n"
	return startDialog(d.path, nil, nil, script)
}

func (d Osa) Kill(shown Shown) { killGroup(shown, d.path) }

const osaActivate = "activate\n"

// 1. Only osascript's final newline is cut so spaces of a secret stay
// 2. Cancel is error -128 on stderr with exit 1 regardless of the position the line names
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

// zenity 3 answers its own timeout with the Cancel code
// No --timeout is given so this process's clock owns the limit
type Zenity struct {
	path string
	env  []string
	// Zero is inputGivesUp
	limit time.Duration
}

func NewZenity(path string, env []string, limit time.Duration) Zenity {
	return Zenity{path: path, env: dialogEnv(env), limit: limit}
}

// zenity's password dialog takes no prompt so only the title names what it asks for
func (d Zenity) Input(tool, _ string) (string, bool, error) {
	r, err := runDialog(d.path, []string{"--password", "--modal", "--title=" + dialogTitle(tool)}, d.env, "", cmp.Or(d.limit, inputGivesUp))
	if err != nil {
		return "", false, err
	}
	return dialogAnswer(parseZenityInput(r))
}

// --text-info keeps the text on stdin while --info exposes it in argv
func (d Zenity) Show(tool, text string) (Shown, error) {
	args := []string{"--text-info", "--title=" + dialogTitle(tool), "--timeout=" + seconds(showGivesUp)}
	return startDialog(d.path, args, d.env, text)
}

func (d Zenity) Kill(shown Shown) { killGroup(shown, d.path) }

// stdout counts only on exit 0 since a timed out entry still prints what was typed
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
		return "", false, errFailed
	}
	return "", false, nil
}

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
