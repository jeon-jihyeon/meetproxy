package main

import (
	"fmt"

	appsignin "github.com/jeon-jihyeon/meetproxy/app/signin"
	model "github.com/jeon-jihyeon/meetproxy/internal/signin"
	"github.com/jeon-jihyeon/meetproxy/internal/signinbrowser"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
)

type signinRecord = store.Signin

var (
	errAutomationDenied = appsignin.ErrAutomationDenied
	errBrowserScript    = appsignin.ErrBrowserScript
	errNoSigninWindow   = appsignin.ErrNoWindow
)

const (
	signinClosed    = "closed"
	signinTimedOut  = "timed out"
	signinWaiting   = "waiting"
	signinNotClosed = "not closed"
	signinGone      = "gone"
	signinAway      = "away"
	signinBack      = "back"
	signinKeep      = model.Keep
	signinGiveUp    = model.GiveUp
)

func linkSum(link string) string        { return signinbrowser.Fingerprint(link) }
func loopbackOrigin(link string) string { return signinbrowser.Origin(link) }
func readSignin(data, session string, row *signinRecord) (bool, error) {
	value, found, err := store.NewSignins(data).Read(session)
	*row = value
	return found, err
}
func writeSignin(data, session string, row signinRecord) error {
	files := store.NewSignins(data)
	if err := files.Prepare(); err != nil {
		return err
	}
	return files.Write(session, row)
}

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
		if exists window id %[2]s then return "open"
	end tell
end if
return %[3]s
`, appleString(app), appleString(window), appleString(signinGone))
}
