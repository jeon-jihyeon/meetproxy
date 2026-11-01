package osdialog

import "errors"

type Shown struct {
	Pid     int    `json:"pid,omitempty"`
	Started string `json:"started,omitempty"`
}

var ErrUnavailable = errors.New("this computer can't show a sign-in dialog")

type Unavailable struct{}

func (Unavailable) Input(string, string) (string, bool, error) { return "", false, ErrUnavailable }
func (Unavailable) Show(string, string) (Shown, error)         { return Shown{}, ErrUnavailable }
func (Unavailable) Kill(Shown)                                 {}
