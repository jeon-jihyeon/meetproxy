package inbox

import (
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/fileio"
)

// A request as its file stores it
// The keys are the stored contract so files written before keep reading
type record struct {
	Id         string    `json:"id"`
	Thread     string    `json:"thread,omitempty"`
	Link       string    `json:"link"`
	Source     string    `json:"source,omitempty"`
	From       string    `json:"from,omitempty"`
	Author     string    `json:"author,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Delegation string    `json:"delegation,omitempty"`
	Task       string    `json:"task,omitempty"`
	Target     string    `json:"target,omitempty"`
	MayApprove bool      `json:"may_approve,omitempty"`
	Ts         string    `json:"ts,omitempty"`
	SessionId  string    `json:"session_id,omitempty"`
	AddedAt    time.Time `json:"added_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	HeldUntil  time.Time `json:"held_until,omitzero"`
	Depth      string    `json:"depth,omitempty"`
	Followup   bool      `json:"followup,omitempty"`
	Correction bool      `json:"correction,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	Acked      []string  `json:"acked,omitempty"`
	Again      bool      `json:"again,omitempty"`
}

// found is false for a missing file
// err with found true means the file does not parse
func (s Store) read(id string) (rec record, found bool, err error) {
	found, err = fileio.ReadJSON(s.itemFile(id), &rec)
	return rec, found, err
}

func (s Store) write(rec record) error { return fileio.WriteJSON(s.itemFile(rec.Id), rec) }
