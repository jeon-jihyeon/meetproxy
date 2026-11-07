package slackevent_test

import (
	"testing"

	listen "github.com/jeon-jihyeon/meetproxy/internal/slackevent"
	"github.com/stretchr/testify/assert"
)

func TestEligible(t *testing.T) {
	t.Parallel()
	valid := listen.Facts{
		ID: "Ev1", App: "A1", Team: "T1", Kind: listen.Mention,
		Channel: "C1", User: "U2", TS: "1893456000.000001",
	}
	direct := valid
	direct.Kind = listen.Direct
	thread := direct
	thread.Thread = "1893455000.000001"
	bot := valid
	bot.Bot = true
	hidden := valid
	hidden.Hidden = true
	self := valid
	self.User = "U1"
	unsupported := valid
	unsupported.Kind = ""
	missing := valid
	missing.ID = ""
	separator := valid
	separator.App = "A1:T1"
	author := valid
	author.User = ""
	timestamp := valid
	timestamp.TS = "1"
	badThread := valid
	badThread.Thread = "invalid"
	type args struct {
		facts listen.Facts
		self  string
	}
	type want struct {
		accepted bool
		err      error
	}
	yes := want{true, nil}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"mention", args{valid, "U1"}, yes},
		{"direct", args{direct, "U1"}, yes},
		{"thread", args{thread, "U1"}, yes},
		{"bot", args{bot, "U1"}, want{}},
		{"hidden", args{hidden, "U1"}, want{}},
		{"own message", args{self, "U1"}, want{}},
		{"unrelated", args{unsupported, "U1"}, want{}},
		{"missing identity", args{missing, "U1"}, want{err: listen.ErrEvent}},
		{"ambiguous identity", args{separator, "U1"}, want{err: listen.ErrEvent}},
		{"missing author", args{author, "U1"}, want{err: listen.ErrEvent}},
		{"invalid timestamp", args{timestamp, "U1"}, want{err: listen.ErrEvent}},
		{"invalid thread", args{badThread, "U1"}, want{err: listen.ErrEvent}},
		{"invalid bot", args{valid, ""}, want{err: listen.ErrEvent}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			accepted, err := tc.args.facts.Eligible(tc.args.self)

			assert.Equal(t, tc.want.accepted, accepted)
			assert.ErrorIs(t, err, tc.want.err)
		})
	}
}
