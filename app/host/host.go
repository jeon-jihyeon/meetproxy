package host

import (
	"encoding/json"
	"slices"
	"time"
)

type Facts struct {
	Name      string
	Can       []string
	Connector bool
	Idle      bool
}

func (f Facts) Has(capability string) bool { return slices.Contains(f.Can, capability) }
func (f Facts) HasConnector() bool         { return f.Has("mcp") && f.Connector }

type Ask struct {
	Header   string   `json:"header"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type MCP struct {
	Server string         `json:"server"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type MCPResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Outcome struct {
	Answer    json.RawMessage
	Dismissed json.RawMessage
	Result    *MCPResult
	Error     *string
}

func (o Outcome) AnswerText() string {
	var answer string
	if json.Unmarshal(o.Answer, &answer) != nil {
		return ""
	}
	return answer
}

type Step struct {
	Result   *string
	Ask      *Ask
	MCP      *MCP
	Chain    string
	State    json.RawMessage
	Fallback *string
	Life     time.Duration
}
