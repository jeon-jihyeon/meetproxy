package store

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type SlackAuth struct {
	User   string    `json:"user"`
	Team   string    `json:"team"`
	Host   string    `json:"host"`
	Scopes []string  `json:"scopes"`
	At     time.Time `json:"at"`
}

func (s Delivery) LockToken() (func(), error) {
	return fileio.Lock(filepath.Join(s.data, "slack", "token.lock"))
}

func (s Delivery) HasToken() bool {
	_, err := os.Stat(filepath.Join(s.data, "slack", "token"))
	return err == nil
}

func (s Delivery) Token() ([]byte, error) {
	return os.ReadFile(filepath.Join(s.data, "slack", "token"))
}

func (s Delivery) Auth() (SlackAuth, bool, error) {
	var a SlackAuth
	found, err := fileio.ReadJSON(filepath.Join(s.data, "slack", "auth.json"), &a)
	return a, found, err
}

func (s Delivery) WriteToken(token []byte) error {
	return fileio.WriteAtomic(filepath.Join(s.data, "slack", "token"), token, 0o600)
}

func (s Delivery) WriteAuth(a SlackAuth) error {
	return fileio.WriteJSON(filepath.Join(s.data, "slack", "auth.json"), a)
}

func (s Delivery) RemoveToken() error {
	for _, name := range []string{"token", "auth.json"} {
		if err := os.Remove(filepath.Join(s.data, "slack", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s Delivery) SaveAuth(token []byte, a SlackAuth) error {
	unlock, err := s.LockToken()
	if err != nil {
		return err
	}
	defer unlock()
	b, err := s.Token()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if token == nil || string(b) != string(token) {
		return nil
	}
	return s.WriteAuth(a)
}
