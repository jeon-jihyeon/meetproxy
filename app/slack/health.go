package slack

import (
	"errors"
	"os"

	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
)

type Health struct {
	Token   []byte
	ByToken bool
	Paused  bool
	Error   string
	Found   int
}

func (a *Account) RecordHealth(in Health) error {
	if in.ByToken {
		unlock, err := a.files.LockToken()
		if err != nil {
			return err
		}
		defer unlock()
		value, err := a.files.Token()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !slackstate.RestoreToken(in.Token).Current(value, err == nil) {
			return nil
		}
	}
	next := slackstate.RestoreHealth(false, in.Error, in.Found, a.now.UTC()).Collect(in.Paused)
	return a.recordHealth(next)
}

func (a *Account) recordHealth(next slackstate.Health) error {
	h := a.readHealth()
	if current, found := h.Sources["slack"]; found {
		old := slackstate.RestoreHealth(current.OK, current.Error, current.Found, current.At)
		if !old.NeedsUpdate(next.OK(), next.Error(), next.Found(), next.At()) {
			return nil
		}
	}
	unlock, err := a.files.LockHealth()
	if err != nil {
		return err
	}
	defer unlock()
	h = a.readHealth()
	h.Sources["slack"] = store.SourceHealth{OK: next.OK(), Error: next.Error(), Found: next.Found(), At: next.At()}
	return a.files.WriteHealth(h)
}

func (a *Account) forgetFailure() error {
	h := a.readHealth()
	current := h.Sources["slack"]
	if !slackstate.RestoreHealth(current.OK, current.Error, current.Found, current.At).Failed() {
		return nil
	}
	unlock, err := a.files.LockHealth()
	if err != nil {
		return err
	}
	defer unlock()
	h = a.readHealth()
	current = h.Sources["slack"]
	if slackstate.RestoreHealth(current.OK, current.Error, current.Found, current.At).Failed() {
		delete(h.Sources, "slack")
	}
	return a.files.WriteHealth(h)
}

func (a *Account) readHealth() store.Health {
	h, err := a.files.Health()
	if err != nil {
		return store.Health{Sources: map[string]store.SourceHealth{}}
	}
	return h
}
