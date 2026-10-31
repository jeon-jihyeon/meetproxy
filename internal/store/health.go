package store

import (
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type SourceHealth struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Found int       `json:"found"`
	At    time.Time `json:"at"`
}

type HookFailure struct {
	Hook  string    `json:"hook"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

type Health struct {
	Sources map[string]SourceHealth `json:"sources"`
	Hooks   []HookFailure           `json:"hooks,omitempty"`
}

func (s Delivery) LockHealth() (func(), error) {
	return fileio.Lock(filepath.Join(s.data, "health.lock"))
}

func (s Delivery) Health() (Health, error) {
	var h Health
	_, err := fileio.ReadJSON(filepath.Join(s.data, "health.json"), &h)
	if h.Sources == nil {
		h.Sources = map[string]SourceHealth{}
	}
	return h, err
}

func (s Delivery) WriteHealth(h Health) error {
	return fileio.WriteJSON(filepath.Join(s.data, "health.json"), h)
}
