package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/signinbrowser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A browser that answers each script in turn and records every script it got
type fakeBrowser struct {
	mu      sync.Mutex
	answers []fakeAnswer
	scripts []string
	at      time.Time
	// Each script hangs until its context ends, the fake clock moving by the time it was given
	hang bool
}

type fakeAnswer struct {
	out string
	err error
}

func (b *fakeBrowser) run(ctx context.Context, script string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scripts = append(b.scripts, script)
	if b.hang {
		if end, ok := ctx.Deadline(); ok {
			b.at = b.at.Add(max(0, time.Until(end)).Round(10 * time.Millisecond))
		}
		return "", errBrowserScript
	}
	if len(b.answers) == 0 {
		return "", nil
	}
	a := b.answers[0]
	if len(b.answers) > 1 {
		b.answers = b.answers[1:]
	}
	return a.out, a.err
}

func (b *fakeBrowser) env() browserScript {
	return browserScript{
		run:   b.run,
		clock: func() time.Time { return b.at },
		sleep: func(d time.Duration) { b.at = b.at.Add(d) },
	}
}

func signinCli(t *testing.T, b *fakeBrowser, out *bytes.Buffer) cli {
	t.Helper()
	c := cli{session: "s1", now: b.at, in: strings.NewReader(""), out: out, data: t.TempDir()}
	if b.answers != nil || b.scripts != nil {
		c.browser = b.env()
	}
	return c
}

// A loopback link carrying a one-time secret in its path
const printedLink = "http://127.0.0.1:38999/0ne-t1me-s3cret?state=st4te"

func writeLog(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.log")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

// Every place a secret could leak to besides the browser script
func assertNoSecret(t *testing.T, data string, out *bytes.Buffer, err error, secrets ...string) {
	t.Helper()
	var files strings.Builder
	_ = filepath.WalkDir(data, func(path string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			files.Write(b)
			files.WriteString(path)
		}
		return nil
	})
	for _, s := range secrets {
		assert.NotContains(t, out.String(), s)
		assert.NotContains(t, files.String(), s)
		if err != nil {
			assert.NotContains(t, err.Error(), s)
		}
	}
}

func TestSigninOpen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	before := "old http://127.0.0.1:1/stale-link\n"
	tests := []struct {
		name    string
		log     string
		offset  int
		browser string
		answers []fakeAnswer
		// The link the script opens, empty when nothing opens
		opens string
		want  string
	}{
		{
			"the newest loopback link after the offset", before + "see https://acme.example.com/x\nopen http://localhost:1/older\nopen " + printedLink + ".\n",
			len(before), "Google Chrome", []fakeAnswer{{out: "1234"}}, printedLink, "opened",
		},
		{"a Whale window", before + printedLink + "\n", len(before), "Whale", []fakeAnswer{{out: "77"}}, printedLink, "opened"},
		{"only a link before the offset", before + "nothing new\n", len(before), "Google Chrome", []fakeAnswer{{out: "1"}}, "", "not found"},
		{"an https link", before + "https://127.0.0.1:1/x\n", len(before), "Google Chrome", []fakeAnswer{{out: "1"}}, "", "not found"},
		{"another host", before + "http://acme.example.com/x http://192.0.2.1/y\n", len(before), "Google Chrome", []fakeAnswer{{out: "1"}}, "", "not found"},
		{"an offset past the end", before, len(before) + 10, "Google Chrome", []fakeAnswer{{out: "1"}}, "", "not found"},
		{"a browser without scripting", before + printedLink + "\n", len(before), "Safari", []fakeAnswer{{out: "1"}}, "", "not found"},
		{"no browser scripting on this OS", before + printedLink + "\n", len(before), "Google Chrome", nil, "", "not found"},
		{
			"Automation denied", before + printedLink + "\n", len(before), "Google Chrome",
			[]fakeAnswer{{err: errAutomationDenied}}, printedLink, "not found",
		},
		{
			"a script that failed", before + printedLink + "\n", len(before), "Google Chrome",
			[]fakeAnswer{{err: errBrowserScript}}, printedLink, "not found",
		},
		{"a window id that is no id", before + printedLink + "\n", len(before), "Google Chrome", []fakeAnswer{{out: "x\"y"}}, printedLink, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBrowser{answers: tt.answers, at: now}
			var out bytes.Buffer
			c := signinCli(t, b, &out)
			log := writeLog(t, tt.log)
			code, err := c.exec("signin", []string{"open", "--log", log, "--offset", strconv.Itoa(tt.offset), "--browser", tt.browser, "--data", c.data})
			require.NoError(t, err)
			assert.Equal(t, 0, code)
			assert.Equal(t, tt.want+"\n", out.String())
			var rec signinRecord
			found, rerr := readSignin(c.data, "s1", &rec)
			require.NoError(t, rerr)
			assert.Equal(t, tt.want == "opened", found)
			if tt.opens == "" {
				assert.Empty(t, b.scripts)
			} else {
				require.Len(t, b.scripts, 1)
				assert.Contains(t, b.scripts[0], `"`+tt.opens+`"`)
				assert.Contains(t, b.scripts[0], `tell application "`+tt.browser+`"`)
				assert.NotContains(t, b.scripts[0], "incognito", "a normal window keeps the user's sessions")
			}
			if found {
				assert.Equal(t, signinRecord{Browser: tt.browser, Window: tt.answers[0].out, At: now, Printed: linkSum(tt.opens), Origin: loopbackOrigin(tt.opens)}, rec)
			}
			assertNoSecret(t, c.data, &out, err, "0ne-t1me-s3cret", "st4te", "stale-link", "127.0.0.1")
		})
	}
}

// The window of an earlier attempt closes only while it still shows it is the sign-in window
func TestSigninOpenClosesTheEarlierWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tabs := tabsScript("Whale", "1")
	tests := []struct {
		name    string
		age     time.Duration
		answers []fakeAnswer
		want    []string
	}{
		{"the earlier sign-in window", time.Minute, []fakeAnswer{{out: "1 http://127.0.0.1:1/stale"}, {}, {out: "2"}}, []string{tabs, closeScript("Whale", "1")}},
		{"the earlier callback on the other loopback name", time.Minute, []fakeAnswer{{out: "1 http://localhost:1/callback"}, {}, {out: "2"}}, []string{tabs, closeScript("Whale", "1")}},
		{"a window of the user under that id", time.Minute, []fakeAnswer{{out: "1 https://acme.example.com/home"}, {out: "2"}}, []string{tabs}},
		{"a local server of the user under that id", time.Minute, []fakeAnswer{{out: "1 http://localhost:3000/app"}, {out: "2"}}, []string{tabs}},
		{"a record older than an id is trusted", signinKeep, []fakeAnswer{{out: "2"}}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBrowser{answers: tt.answers, at: now}
			var out bytes.Buffer
			c := signinCli(t, b, &out)
			require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Whale", Window: "1", At: now.Add(-tt.age), Origin: loopbackOrigin("http://127.0.0.1:1/stale")}))
			_, err := c.exec("signin", []string{"open", "--log", writeLog(t, printedLink), "--offset", "0", "--browser", "Google Chrome", "--data", c.data})
			require.NoError(t, err)
			require.Len(t, b.scripts, len(tt.want)+1)
			assert.Equal(t, tt.want, b.scripts[:len(tt.want)])
			assert.Contains(t, b.scripts[len(tt.want)], "make new window")
			var rec signinRecord
			_, err = readSignin(c.data, "s1", &rec)
			require.NoError(t, err)
			assert.Equal(t, "2", rec.Window)
		})
	}
}

func TestSigninWait(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	code := "http://127.0.0.1:38999/callback?code=c0de-v4lue&state=st4te"
	idp := "false https://idp.example.com/login"
	tests := []struct {
		name string
		// How long before now signin open ran
		age     time.Duration
		step    string
		answers []fakeAnswer
		want    string
		// The record stays for the next run
		kept   bool
		closes bool
	}{
		{
			"the callback then a page past loopback", 0, "",
			[]fakeAnswer{{out: "false " + printedLink}, {out: idp}, {out: "true " + code}, {out: "false https://acme.example.com/ok"}, {out: signinGone}},
			"closed", false, true,
		},
		{
			"the callback then a page past loopback the close left open", 0, "",
			[]fakeAnswer{{out: "false " + printedLink}, {out: idp}, {out: "true " + code}, {out: "false https://acme.example.com/ok"}, {out: "open"}},
			"not closed", false, true,
		},
		{
			"the callback loaded in a tracked window the close left open", 0, "",
			[]fakeAnswer{{out: "false " + printedLink}, {out: idp}, {out: "true " + code}, {out: "false " + code}, {out: "open"}},
			"not closed", true, true,
		},
		{"the callback page loaded", 0, "", []fakeAnswer{{out: idp}, {out: "false " + code}, {out: "1 " + code}, {out: signinGone}}, "closed", false, true},
		{"a callback window the close left open", 0, "", []fakeAnswer{{out: idp}, {out: "false " + code}, {out: "1 " + code}, {out: "open"}}, "not closed", true, true},
		{"a close script that failed", 0, "", []fakeAnswer{{out: idp}, {out: "false " + code}, {out: "1 " + code}, {err: errBrowserScript}}, "not closed", true, true},
		{"a local server of the user after a relaunch", 0, signinAway, []fakeAnswer{{out: "true http://localhost:3000/"}, {out: "false http://localhost:3000/"}, {out: "1 http://localhost:3000/"}}, "closed", false, false},
		{"a page of the user then a local server of the user", 0, "", []fakeAnswer{{out: "false https://mail.example.com/"}, {out: "false http://localhost:5173/"}, {out: "1 http://localhost:5173/"}}, "closed", false, false},
		{"a saved step then a page of the user then a local server of the user", 0, signinAway, []fakeAnswer{{out: "false https://mail.example.com/"}, {out: "false http://localhost:5173/"}, {out: "1 http://localhost:5173/"}}, "closed", false, false},
		{"a failed read before a callback saved by an earlier run", 0, signinBack, []fakeAnswer{{err: errBrowserScript}, {out: "false https://acme.example.com/home"}, {out: "1 https://acme.example.com/home"}}, "closed", false, false},
		{"a step saved by an earlier run", 0, signinAway, []fakeAnswer{{out: "false " + code}, {out: "1 " + code}, {out: signinGone}}, "closed", false, true},
		{"a callback saved by an earlier run, then a page of the user", 0, signinBack, []fakeAnswer{{out: "false https://acme.example.com/home"}, {out: "1 https://acme.example.com/home"}}, "closed", false, false},
		{"a callback saved by an earlier run, then a window of several tabs", 0, signinBack, []fakeAnswer{{out: "false " + code}, {out: "3 " + code}}, "closed", false, false},
		{"the window gone", 0, "", []fakeAnswer{{out: signinGone}}, "closed", false, false},
		{"still at the identity provider", 0, "", []fakeAnswer{{out: idp}}, "waiting", true, false},
		{"still on the printed link", 0, "", []fakeAnswer{{out: "false " + printedLink}}, "waiting", true, false},
		{"reads that fail", 0, "", []fakeAnswer{{err: errBrowserScript}}, "waiting", true, false},
		{"the give-up time on the printed link", signinGiveUp, "", []fakeAnswer{{out: "1 " + printedLink}, {out: signinGone}}, "timed out", false, true},
		{"the give-up close left open", signinGiveUp, "", []fakeAnswer{{out: "1 " + printedLink}, {out: "open"}}, "not closed", true, true},
		{"the give-up time at the identity provider leaves the window", signinGiveUp, "", []fakeAnswer{{out: "1 https://idp.example.com/login"}}, "timed out", false, false},
		{"the give-up time during the run", signinGiveUp - 10*time.Second, "", []fakeAnswer{{out: idp}}, "timed out", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBrowser{answers: tt.answers, at: now}
			var out bytes.Buffer
			c := signinCli(t, b, &out)
			require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1234", At: now.Add(-tt.age), Step: tt.step, Origin: loopbackOrigin(printedLink)}))
			exit, err := c.exec("signin", []string{"wait", "--data", c.data})
			require.NoError(t, err)
			assert.Equal(t, 0, exit)
			assert.Equal(t, tt.want+"\n", out.String())
			assert.LessOrEqual(t, b.at.Sub(now), 100*time.Second)
			var rec signinRecord
			found, err := readSignin(c.data, "s1", &rec)
			require.NoError(t, err)
			assert.Equal(t, tt.kept, found)
			closed := false
			for _, s := range b.scripts {
				assert.Contains(t, s, `window id "1234"`)
				assert.NotContains(t, s, "http")
				if strings.Contains(s, "close window") {
					closed = true
					assert.NotContains(t, s, "URL", "the close asks only whether the window still exists")
				}
			}
			assert.Equal(t, tt.closes, closed)
			assertNoSecret(t, c.data, &out, err, "c0de-v4lue", "st4te", "0ne-t1me-s3cret", "idp.example.com")
		})
	}
}

// A record signin wait keeps after a close that left the window open lets hook end close that window
func TestSigninWait_NotClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	code := "http://127.0.0.1:38999/callback?code=c0de-v4lue&state=st4te"
	idp := "false https://idp.example.com/login"

	tests := []struct {
		name    string
		age     time.Duration
		answers []fakeAnswer
		// What the window shows when the session ends
		left string
	}{
		{"a tracked callback page", 0, []fakeAnswer{{out: "false " + printedLink}, {out: idp}, {out: "true " + code}, {out: "false " + code}, {out: "open"}}, "1 " + code},
		{"a callback page", 0, []fakeAnswer{{out: idp}, {out: "false " + code}, {out: "1 " + code}, {out: "open"}}, "1 " + code},
		{"the give-up time on the printed link", signinGiveUp, []fakeAnswer{{out: "1 " + printedLink}, {out: "open"}}, "1 " + printedLink},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBrowser{answers: tt.answers, at: now}
			var out bytes.Buffer
			c := signinCli(t, b, &out)
			require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1234", At: now.Add(-tt.age), Origin: loopbackOrigin(printedLink)}))
			_, err := c.exec("signin", []string{"wait", "--data", c.data})
			require.NoError(t, err)
			require.Equal(t, signinNotClosed+"\n", out.String())

		})
	}
}

// A provider that redirected back before the first read still ends at the callback, never at the give-up time
func TestSigninWait_CallbackBeforeTheFirstRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := &fakeBrowser{answers: []fakeAnswer{{out: "false http://127.0.0.1:38999/callback?code=c0de-v4lue"}, {}}, at: now}
	var out bytes.Buffer
	c := signinCli(t, b, &out)
	require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1234", At: now, Printed: linkSum(printedLink)}))

	_, err := c.exec("signin", []string{"wait", "--data", c.data})

	require.NoError(t, err)
	assert.Equal(t, signinClosed+"\n", out.String())
}

// The printed link in the form the browser shows it is still the printed link, never the callback
func TestSigninWait_BrowserFormOfThePrintedLinkWaits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct{ printed, shown string }{
		{"http://localhost:1234?state=x", "http://localhost:1234/?state=x"},
		{"http://LOCALHOST:80/cb?b=2&a='1'", "http://localhost/cb?a=%271%27&b=2"},
	}
	for _, tt := range tests {
		b := &fakeBrowser{answers: []fakeAnswer{{out: "true " + tt.shown}}, at: now}
		var out bytes.Buffer
		c := signinCli(t, b, &out)
		require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1234", At: now, Printed: linkSum(tt.printed)}))

		_, err := c.exec("signin", []string{"wait", "--data", c.data})

		require.NoError(t, err)
		assert.Equal(t, "waiting\n", out.String(), tt.printed)
		var rec signinRecord
		_, err = readSignin(c.data, "s1", &rec)
		require.NoError(t, err)
		assert.Empty(t, rec.Step, tt.printed)
	}
}

func TestSigninWaitStep(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := &fakeBrowser{answers: []fakeAnswer{{out: "false https://idp.example.com/login"}}, at: now}
	var out bytes.Buffer
	c := signinCli(t, b, &out)
	require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Whale", Window: "9", At: now}))
	_, err := c.exec("signin", []string{"wait", "--data", c.data})
	require.NoError(t, err)
	var rec signinRecord
	_, err = readSignin(c.data, "s1", &rec)
	require.NoError(t, err)
	assert.Equal(t, signinRecord{Browser: "Whale", Window: "9", At: now, Step: signinAway}, rec)
}

func TestSigninWaitFails(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t.Run("no window opened", func(t *testing.T) {
		t.Parallel()
		b := &fakeBrowser{answers: []fakeAnswer{}, at: now}
		var out bytes.Buffer
		c := signinCli(t, b, &out)
		code, err := c.exec("signin", []string{"wait", "--data", c.data})
		require.ErrorIs(t, err, errNoSigninWindow)
		assert.Equal(t, exitFailed, code)
		assert.Empty(t, out.String())
	})
	t.Run("Automation denied", func(t *testing.T) {
		t.Parallel()
		b := &fakeBrowser{answers: []fakeAnswer{{err: errAutomationDenied}}, at: now}
		var out bytes.Buffer
		c := signinCli(t, b, &out)
		require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1", At: now}))
		code, err := c.exec("signin", []string{"wait", "--data", c.data})
		require.ErrorIs(t, err, errAutomationDenied)
		assert.Equal(t, exitFailed, code)
		assert.Empty(t, out.String())
	})
	t.Run("a session from the environment only", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		c := cli{out: &out, in: strings.NewReader("")}
		code, err := c.exec("signin", []string{"wait", "--data", t.TempDir()})
		require.ErrorIs(t, err, errUsage)
		assert.Equal(t, exitUsage, code)
	})
}

func TestNewestLoopbackLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, text, want string
	}{
		{"quoted", `url="` + printedLink + `"`, printedLink},
		{"in parentheses", "(" + printedLink + ")", printedLink},
		{"after a color code", "\x1b[1m" + printedLink + "\x1b[0m", printedLink},
		{"localhost with a port", "go to http://localhost:8085/a/b.", "http://localhost:8085/a/b"},
		{"a lookalike host", "http://127.0.0.1.example.com/x", ""},
		{"user info", "http://ann@127.0.0.1/x", ""},
		{"none", "nothing here", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, signinbrowser.LinkAfter(writeLog(t, tt.text), 0))
		})
	}
}

func TestOsascriptCommand(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("no osascript")
	}
	script := "return 1"
	cmd := signinbrowser.Command(context.Background(), script)
	assert.Equal(t, []string{"/usr/bin/osascript"}, cmd.Args)
	assert.NotZero(t, cmd.WaitDelay)
	b, err := io.ReadAll(cmd.Stdin)
	require.NoError(t, err)
	assert.Equal(t, script, string(b))
}

// Reads and closes that hang until their timeout still end the run within 100 s
func TestSigninWait_HangingScriptsEndWithinTheRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		// How long before now signin open ran
		age  time.Duration
		want string
	}{
		{"reads that hang", 0, signinWaiting},
		{"the give-up close after a hanging read", signinGiveUp - 85*time.Second, signinTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBrowser{answers: []fakeAnswer{{}}, at: now, hang: true}
			var out bytes.Buffer
			c := signinCli(t, b, &out)
			require.NoError(t, writeSignin(c.data, "s1", signinRecord{Browser: "Google Chrome", Window: "1234", At: now.Add(-tt.age), Origin: loopbackOrigin(printedLink)}))

			_, err := c.exec("signin", []string{"wait", "--data", c.data})

			require.NoError(t, err)
			assert.Equal(t, tt.want+"\n", out.String())
			assert.LessOrEqual(t, b.at.Sub(now), 100*time.Second)
		})
	}
}
