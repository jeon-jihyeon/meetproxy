package control_test

import (
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/control"
	"github.com/stretchr/testify/assert"
)

func TestOperation(t *testing.T) {
	t.Parallel()
	type args struct {
		id, tool, session string
	}
	tcs := []struct {
		name  string
		args  args
		valid bool
	}{
		{name: "read without workflow", args: args{tool: "list_apps", session: "s1"}, valid: true},
		{name: "effect with operation", args: args{id: "op1", tool: "click", session: "s1"}, valid: true},
		{name: "additional adapter", args: args{id: "op1", tool: "send_message", session: "s1"}, valid: true},
		{name: "effect without operation", args: args{tool: "click", session: "s1"}},
		{name: "missing session", args: args{id: "op1", tool: "click"}},
		{name: "invalid tool", args: args{id: "op1", tool: "click;execute", session: "s1"}},
		{name: "invalid operation", args: args{id: "../other", tool: "click", session: "s1"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := control.New(tc.args.id, tc.args.tool, tc.args.session)
			assert.Equal(t, tc.valid, err == nil)
		})
	}
}

func TestOutcome(t *testing.T) {
	t.Parallel()
	type args struct {
		started, succeeded bool
	}
	tcs := []struct {
		name string
		args args
		want control.State
	}{
		{name: "process never started", want: control.NotStarted},
		{name: "acknowledged call", args: args{started: true, succeeded: true}, want: control.Completed},
		{name: "lost response", args: args{started: true}, want: control.Unknown},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			op, err := control.New("op1", "click", "s1")
			assert.NoError(t, err)
			assert.Equal(t, tc.want, op.Outcome(tc.args.started, tc.args.succeeded))
		})
	}
}
