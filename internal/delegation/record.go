package delegation

import (
	"fmt"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// A user delegation as delegations.json stores it
type record struct {
	Id            string   `json:"id"`
	When          string   `json:"when"`
	Channel       string   `json:"channel,omitempty"`
	Host          string   `json:"host,omitempty"`
	From          []string `json:"from,omitempty"`
	Words         []string `json:"words,omitempty"`
	Link          string   `json:"link,omitempty"`
	Do            string   `json:"do"`
	Approve       string   `json:"approve,omitempty"`
	Note          string   `json:"note,omitempty"`
	MaxPerHour    int      `json:"max_per_hour,omitempty"`
	DedupeMinutes int      `json:"dedupe_minutes,omitempty"`
}

func (s Store) load() ([]record, error) {
	var rs []record
	found, err := fileio.ReadJSON(s.file, &rs)
	if err != nil && found {
		return nil, fmt.Errorf("%s is corrupt: %w", s.file, err)
	}
	return rs, err
}

func (s Store) save(rs []record) error { return fileio.WriteJSON(s.file, rs) }
