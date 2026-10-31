package store

import "regexp"

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type Signin struct {
	Browser string    `json:"browser"`
	Window  string    `json:"window"`
	At      time.Time `json:"at"`
	Step    string    `json:"step,omitempty"`
	Printed string    `json:"printed,omitempty"`
	Origin  string    `json:"origin,omitempty"`
}

type Signins struct{ dir string }

func NewSignins(data string) Signins { return Signins{dir: filepath.Join(data, "signin")} }

func (s Signins) path(session string) (string, error) {
	if !sessionID.MatchString(session) {
		return "", os.ErrInvalid
	}
	return filepath.Join(s.dir, session+".json"), nil
}

func (s Signins) Read(session string) (Signin, bool, error) {
	var row Signin
	path, err := s.path(session)
	if err != nil {
		return row, false, err
	}
	found, err := fileio.ReadJSON(path, &row)
	return row, found, err
}

func (s Signins) Write(session string, row Signin) error {
	path, err := s.path(session)
	if err != nil {
		return err
	}
	if err := s.Prepare(); err != nil {
		return err
	}
	return fileio.WriteJSON(path, row)
}

func (s Signins) Prepare() error { return os.MkdirAll(s.dir, 0o700) }

func (s Signins) Remove(session string) error {
	path, err := s.path(session)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
