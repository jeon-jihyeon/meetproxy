package main

import "regexp"
import "errors"

var sessionId = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type osDialog interface {
	Input(string, string) (string, bool, error)
	Show(string, string) (shownDialog, error)
	Kill(shownDialog)
}
type shownDialog struct {
	Pid     int    `json:"pid,omitempty"`
	Started string `json:"started,omitempty"`
}

var errNoDialog = errors.New("this computer can't show a sign-in dialog")

type noDialog struct{}

func (noDialog) Input(string, string) (string, bool, error) { return "", false, errNoDialog }
func (noDialog) Show(string, string) (shownDialog, error)   { return shownDialog{}, errNoDialog }
func (noDialog) Kill(shownDialog)                           {}
