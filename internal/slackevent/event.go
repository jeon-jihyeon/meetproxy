package slackevent

import (
	"errors"
	"strings"
)

type Kind string

const (
	Mention Kind = "mention"
	Direct  Kind = "direct_message"
)

var ErrEvent = errors.New("invalid listen event")

type Facts struct {
	ID      string
	App     string
	Team    string
	Kind    Kind
	Channel string
	User    string
	TS      string
	Thread  string
	Bot     bool
	Hidden  bool
}

func (f Facts) Eligible(self string) (bool, error) {
	if !ValidUser(self) {
		return false, ErrEvent
	}
	if f.Kind != Mention && f.Kind != Direct || f.Bot || f.Hidden || f.User == self {
		return false, nil
	}
	if !validIdentity(f.ID) || !validIdentity(f.App) || !validIdentity(f.Team) ||
		!validIdentity(f.Channel) || !ValidUser(f.User) || !validTimestamp(f.TS) {
		return false, ErrEvent
	}
	if f.Thread != "" && !validTimestamp(f.Thread) {
		return false, ErrEvent
	}
	return true, nil
}

func ValidUser(id string) bool {
	if len(id) < 2 || len(id) > 32 || id[0] != 'U' && id[0] != 'W' {
		return false
	}
	for _, value := range id[1:] {
		if value < 'A' || value > 'Z' {
			if value < '0' || value > '9' {
				return false
			}
		}
	}
	return true
}

func validIdentity(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, value := range id {
		if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
			continue
		}
		return false
	}
	return true
}

func validTimestamp(ts string) bool {
	whole, fraction, found := strings.Cut(ts, ".")
	if !found || len(whole) < 1 || len(whole) > 20 || len(fraction) != 6 {
		return false
	}
	for _, value := range whole + fraction {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}
