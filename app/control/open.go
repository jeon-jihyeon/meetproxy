package control

import (
	"errors"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/cuadriver"
	"github.com/jeon-jihyeon/meetproxy/internal/opstore"
)

type Config struct {
	Binary      string
	Environment []string
	Timeout     time.Duration
	Data        string
	Journal     Journal
}

func Open(config Config) (*Controller, error) {
	journal := config.Journal
	if absent(journal) {
		journal = nil
	}
	if config.Data != "" && journal != nil {
		return nil, errors.New("choose a local operation directory or a consumer journal")
	}
	if config.Data != "" {
		journal = opstore.New(config.Data)
	}
	return New(cuadriver.New(config.Binary, config.Environment, config.Timeout), journal), nil
}
