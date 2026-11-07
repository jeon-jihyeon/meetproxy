package control

import "errors"

type State string

const (
	NotStarted State = "not_started"
	Completed  State = "completed"
	Unknown    State = "unknown"
)

var ErrInput = errors.New("invalid control request")

type Operation struct {
	id   string
	tool string
}

func New(id, tool, session string) (Operation, error) {
	op := Operation{id: id, tool: tool}
	if !identifier(tool, 80) || !identifier(session, 128) {
		return Operation{}, ErrInput
	}
	if id != "" && !identifier(id, 128) {
		return Operation{}, ErrInput
	}
	if op.Effectful() && id == "" {
		return Operation{}, ErrInput
	}
	return op, nil
}

func (o Operation) ID() string   { return o.id }
func (o Operation) Tool() string { return o.tool }

func (o Operation) Effectful() bool {
	switch o.tool {
	case "list_apps", "list_windows", "get_window_state":
		return false
	default:
		return true
	}
}

func (o Operation) Outcome(started, succeeded bool) State {
	if !started {
		return NotStarted
	}
	if succeeded {
		return Completed
	}
	return Unknown
}

func identifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}
