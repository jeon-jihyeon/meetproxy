package main

import "regexp"

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
