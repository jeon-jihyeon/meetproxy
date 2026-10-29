package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// Browser scripting of signin open and signin wait
// The link and the window's address reach only the script and its stdout, never argv, the output or an error
type browserScript struct {
	// Runs an AppleScript given on stdin and returns its stdout without one trailing newline
	// nil where the OS has no browser scripting
	run   func(ctx context.Context, script string) (string, error)
	clock func() time.Time
	sleep func(time.Duration)
}

var (
	// The browser's Automation permission was denied, AppleScript error -1743
	errAutomationDenied = errors.New("the browser's Automation permission for meetproxy was denied")
	// Holds no stderr since an AppleScript error can quote the script and so the link
	errBrowserScript  = errors.New("the browser script failed")
	errNoSigninWindow = errors.New("no sign-in window is open for this session")
)

const (
	signinOpened   = "opened"
	signinNotFound = "not found"
	signinClosed   = "closed"
	signinTimedOut = "timed out"
	signinWaiting  = "waiting"
	// What the read script answers when the window or the browser is gone
	signinGone = "gone"
	// The window left loopback for the identity provider
	signinAway = "away"
	// The window came back to loopback for the callback while it loaded
	signinBack = "back"
	// How long after signin open the window is given up on
	// It closes only while closeSignin proves it is still the sign-in window
	signinGiveUp = 5 * time.Minute
	// How long a record's window id is trusted to name the window signin open opened
	signinKeep = 10 * time.Minute
	// A wait run starts no read after 90 s
	signinRun = 90 * time.Second
	// Every script of a wait run is cut here so the run returns within 100 s
	signinLimit = 100 * time.Second
	// Between two reads of the window's address
	signinPoll = 500 * time.Millisecond
	// signin open may wait on macOS's Automation consent prompt the first time
	signinOpenTimeout = 2 * time.Minute
	signinReadTimeout = 10 * time.Second
	// The most of the log after the offset that is read, from its end
	signinLogTail = 1 << 20
)

// Browsers whose scripting dictionary is Chrome's: make new window, active tab and window id
var scriptedBrowsers = map[string]string{
	"google chrome": "Google Chrome",
	"chrome":        "Google Chrome",
	"whale":         "Whale",
	"naver whale":   "Whale",
}

// The window signin open opened, never its link or address
type signinRecord struct {
	Browser string    `json:"browser"`
	Window  string    `json:"window"`
	At      time.Time `json:"at"`
	// How far the window got, empty while it still shows the printed link
	Step string `json:"step,omitempty"`
	// The sha256 of the printed link, so a loopback address that differs from it is the callback even when no step away was seen
	Printed string `json:"printed,omitempty"`
	// The sha256 of the printed link's loopback port, so only a page of that server counts as the sign-in window
	Origin string `json:"origin,omitempty"`
}

// The sha256 of a link in the form a browser shows it, so the printed link and its address in the window sum alike
func linkSum(link string) string {
	sum := sha256.Sum256([]byte(browserForm(link)))
	return hex.EncodeToString(sum[:])
}

// A link with what a browser changes when it shows one made the same
// 1. The scheme and the host lowercased and a default port dropped
// 2. An empty path is /
// 3. The path, the query values and the fragment compared decoded, the query sorted by key
func browserForm(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), defaultPort(u)
	if port != "" {
		host += ":" + port
	}
	path := cmpOr(u.Path, "/")
	return scheme + "://" + host + path + "?" + u.Query().Encode() + "#" + u.Fragment
}

// A link's port, empty when it is its scheme's default
func defaultPort(u *url.URL) string {
	scheme, port := strings.ToLower(u.Scheme), u.Port()
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		return ""
	}
	return port
}

// The sha256 of a loopback link's port, empty for any other link
// 127.0.0.1 and localhost sum alike since a provider may call back on either
func loopbackOrigin(link string) string {
	u, err := url.Parse(link)
	if err != nil || !isLoopback(link) {
		return ""
	}
	sum := sha256.Sum256([]byte("http://loopback:" + cmpOr(defaultPort(u), "80")))
	return hex.EncodeToString(sum[:])
}

// Where signin open keeps the browser window it opened, never its link or address
func signinFile(data, session string) string {
	return filepath.Join(data, "signin", session+".json")
}

// hook end removes the session's record whatever the commands left
func removeSignin(data, session string) error {
	if !sessionId.MatchString(session) {
		return nil
	}
	err := os.Remove(signinFile(data, session))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func readSignin(data, session string, rec *signinRecord) (bool, error) {
	return fileio.ReadJSON(signinFile(data, session), rec)
}

func writeSignin(data, session string, rec signinRecord) error {
	if err := os.MkdirAll(filepath.Join(data, "signin"), 0o700); err != nil {
		return err
	}
	return fileio.WriteJSON(signinFile(data, session), rec)
}

var windowId = regexp.MustCompile(`^[0-9]{1,20}$`)

// Opens the newest loopback link the log got after offset in a new window of the browser
// A normal window keeps the user's existing sessions, so a sign-in they already have goes through without them
// Every way it can't prints not found, so opening the link stays the user's step
func (c cli) signinOpen(log, offset, browser string) error {
	at, err := strconv.ParseInt(offset, 10, 64)
	if err != nil || at < 0 {
		return fmt.Errorf("%w: --offset takes the log's size in bytes", errUsage)
	}
	app, ok := scriptedBrowsers[strings.ToLower(strings.TrimSpace(browser))]
	link := ""
	if ok && c.browser.run != nil {
		link = loopbackLinkAfter(log, at)
	}
	if link == "" {
		return c.print(signinNotFound)
	}
	var old signinRecord
	if found, err := readSignin(c.data, c.session, &old); err == nil && found {
		// The window of an earlier attempt holds a stale page and maybe a used code
		c.closeSignin(old, c.now, time.Time{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), signinOpenTimeout)
	defer cancel()
	id, err := c.browser.run(ctx, openScript(app, link))
	if err != nil || !windowId.MatchString(id) {
		return c.print(signinNotFound)
	}
	rec := signinRecord{Browser: app, Window: id, At: c.now, Printed: linkSum(link), Origin: loopbackOrigin(link)}
	if err := writeSignin(c.data, c.session, rec); err != nil {
		// A window no record names would hold the callback code until the user closes it
		c.closeWindow(rec, time.Time{})
		return err
	}
	return c.print(signinOpened)
}

// The newest loopback link in the log after offset, empty when there is none or the log can't be read
func loopbackLinkAfter(log string, offset int64) string {
	f, err := os.Open(log)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || offset > info.Size() {
		return ""
	}
	from := max(offset, info.Size()-signinLogTail)
	b, err := io.ReadAll(io.NewSectionReader(f, from, info.Size()-from))
	if err != nil {
		return ""
	}
	return newestLoopbackLink(b)
}

// Only characters a URL may hold, so the link never holds a quote or a backslash that could end the script's string
var httpLink = regexp.MustCompile(`http://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

func newestLoopbackLink(b []byte) string {
	links := httpLink.FindAll(b, -1)
	for i := len(links) - 1; i >= 0; i-- {
		link := strings.TrimRight(string(links[i]), ".,;:!?')]")
		if isLoopback(link) && !strings.Contains(link, "@") {
			return link
		}
	}
	return ""
}

func isLoopback(link string) bool {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}

// Reads the window signin open opened until the callback is done or the give-up time passed
// Each run returns within 100 s and prints waiting while neither happened
func (c cli) signinWait() error {
	var rec signinRecord
	found, err := readSignin(c.data, c.session, &rec)
	if err != nil {
		return err
	}
	if !found || c.browser.run == nil {
		return errNoSigninWindow
	}
	start := c.browser.clock()
	end, limit := start.Add(signinRun), start.Add(signinLimit)
	// The read before this one ran in this run and saw the window on the printed link's loopback port
	// Only then does its id still name the sign-in window when the next read ends the callback
	tracked := false
	for {
		// Read before each script, since the read before may have taken its whole timeout
		now := c.browser.clock()
		if !now.Before(rec.At.Add(signinGiveUp)) {
			c.closeSignin(rec, now, limit)
			return c.endSignin(signinTimedOut)
		}
		if !now.Before(end) {
			return c.print(signinWaiting)
		}
		step, address, err := c.readWindow(rec, limit)
		switch {
		case errors.Is(err, errAutomationDenied):
			return err
		case step == signinGone:
			return c.endSignin(signinClosed)
		case step == signinClosed && tracked:
			// The read right before saw this window on the callback's server
			c.closeWindow(rec, limit)
			return c.endSignin(signinClosed)
		case step == signinClosed:
			// A step saved by an earlier run or a read off the printed port may name a window the browser gave again after a relaunch
			c.closeSignin(rec, now, limit)
			return c.endSignin(signinClosed)
		case step != rec.Step:
			rec.Step = step
			if err := writeSignin(c.data, c.session, rec); err != nil {
				return err
			}
		}
		tracked = err == nil && rec.onOrigin(address)
		c.browser.sleep(signinPoll)
	}
}

// The context of one browser script, cut at limit when it isn't zero
func (c cli) scriptContext(limit time.Time) (context.Context, context.CancelFunc) {
	d := signinReadTimeout
	if !limit.IsZero() {
		d = min(d, limit.Sub(c.browser.clock()))
	}
	return context.WithTimeout(context.Background(), d)
}

// The step the window is at now, from the step it had, and the address it shows
// A read that failed leaves the step as it was
func (c cli) readWindow(rec signinRecord, limit time.Time) (string, string, error) {
	ctx, cancel := c.scriptContext(limit)
	defer cancel()
	out, err := c.browser.run(ctx, readScript(rec.Browser, rec.Window))
	if err != nil {
		return rec.Step, "", err
	}
	if out == signinGone {
		return signinGone, "", nil
	}
	loading, address, _ := strings.Cut(out, " ")
	step := rec.Step
	// A loopback address other than the printed link is the callback, even when the provider redirected before any read saw it
	if step == "" && isLoopback(address) && rec.Printed != "" && linkSum(address) != rec.Printed {
		step = signinAway
	}
	return nextStep(step, address, loading == "true"), address, nil
}

// Whether an address is on the printed link's loopback port, never true for a record without it
func (rec signinRecord) onOrigin(address string) bool {
	return rec.Origin != "" && loopbackOrigin(address) == rec.Origin
}

// 1. Leaving loopback first means the identity provider
// 2. Loopback after that is the callback, done once its page loaded
// 3. Leaving loopback after the callback is done too
func nextStep(step, address string, loading bool) string {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return step
	}
	loopback := isLoopback(address)
	switch {
	case step == "" && !loopback:
		return signinAway
	case step == signinAway && loopback && loading:
		return signinBack
	case step == signinAway && loopback, step == signinBack && (!loading || !loopback):
		return signinClosed
	}
	return step
}

func (c cli) endSignin(word string) error {
	if err := removeSignin(c.data, c.session); err != nil {
		return err
	}
	return c.print(word)
}

// Closes the window a record names only while it still shows it is the sign-in window
// A browser launched again counts its window ids from the start, so the id alone may name a window of the user
// 1. The record is younger than signinKeep
// 2. The window holds one tab whose address is on the printed link's loopback port
// Anything else leaves the window to the user
// Its scripts are cut at limit when it isn't zero
func (c cli) closeSignin(rec signinRecord, now, limit time.Time) {
	if !c.scripted(rec) || rec.Origin == "" || !now.Before(rec.At.Add(signinKeep)) {
		return
	}
	ctx, cancel := c.scriptContext(limit)
	defer cancel()
	out, err := c.browser.run(ctx, tabsScript(rec.Browser, rec.Window))
	tabs, address, _ := strings.Cut(out, " ")
	if err != nil || tabs != "1" || !rec.onOrigin(address) {
		return
	}
	c.closeWindow(rec, limit)
}

// Closes the window by its id, a failure leaving it to the user
func (c cli) closeWindow(rec signinRecord, limit time.Time) {
	if !c.scripted(rec) {
		return
	}
	ctx, cancel := c.scriptContext(limit)
	defer cancel()
	_, _ = c.browser.run(ctx, closeScript(rec.Browser, rec.Window))
}

func (c cli) scripted(rec signinRecord) bool {
	_, ok := scriptedBrowsers[strings.ToLower(rec.Browser)]
	return ok && windowId.MatchString(rec.Window) && c.browser.run != nil
}

func (c cli) print(word string) error {
	_, err := fmt.Fprintln(c.out, word)
	return err
}

// The window id is text in Chrome's dictionary, hence the quotes
func openScript(app, link string) string {
	return fmt.Sprintf(`tell application %s
	set w to make new window
	set URL of active tab of w to %s
	return id of w
end tell
`, appleString(app), appleString(link))
}

// is running sends no Apple event, so a browser the user quit is not launched again
func readScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then
			set t to active tab of window id %[2]s
			return (loading of t as text) & " " & (URL of t)
		end if
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(signinGone))
}

// The tab count and the active tab's address of the window, or gone
func tabsScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then
			set w to window id %[2]s
			return ((count of tabs of w) as text) & " " & (URL of active tab of w)
		end if
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(signinGone))
}

func closeScript(app, window string) string {
	return fmt.Sprintf(`if application %[1]s is running then
	tell application %[1]s
		if exists window id %[2]s then close window id %[2]s
	end tell
end if
`, appleString(app), appleString(window))
}
