package store

import (
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

type SlackUser struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Names   []string  `json:"names,omitempty"`
	Deleted bool      `json:"deleted,omitempty"`
	At      time.Time `json:"at"`
}

type Delivery struct{ data string }

func NewDelivery(data string) Delivery { return Delivery{data: data} }

func (s Delivery) ConnectorHost() string {
	var a struct {
		Host string `json:"host"`
	}
	_, _ = fileio.ReadJSON(filepath.Join(s.data, "slack", "connector.json"), &a)
	return a.Host
}

func (s Delivery) Users() map[string]SlackUser {
	out := map[string]SlackUser{}
	if _, err := fileio.ReadJSON(filepath.Join(s.data, "slack", "users.json"), &out); err != nil {
		return map[string]SlackUser{}
	}
	return out
}

func (s Delivery) SaveUsers(users map[string]SlackUser) error {
	return fileio.WriteJSON(filepath.Join(s.data, "slack", "users.json"), users)
}
