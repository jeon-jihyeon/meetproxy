package listen_test

import (
	"strings"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/listen"
	"github.com/stretchr/testify/assert"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	type args struct{ source, id, kind string }
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"generic", args{"acme:files", "1", "changed"}, nil},
		{"unicode", args{"acme", "사건", "변경"}, nil},
		{"limits", args{strings.Repeat("s", 256), strings.Repeat("i", 256), strings.Repeat("k", 128)}, nil},
		{"empty source", args{"", "1", "changed"}, listen.ErrEvent},
		{"empty id", args{"acme", "", "changed"}, listen.ErrEvent},
		{"blank kind", args{"acme", "1", "  "}, listen.ErrEvent},
		{"source limit", args{strings.Repeat("s", 257), "1", "changed"}, listen.ErrEvent},
		{"id limit", args{"acme", strings.Repeat("i", 257), "changed"}, listen.ErrEvent},
		{"kind limit", args{"acme", "1", strings.Repeat("k", 129)}, listen.ErrEvent},
		{"control", args{"acme", "1\n", "changed"}, listen.ErrEvent},
		{"unicode control", args{"acme", "1", "changed\u0085"}, listen.ErrEvent},
		{"invalid UTF8", args{"acme", string([]byte{255}), "changed"}, listen.ErrEvent},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, listen.Validate(tc.args.source, tc.args.id, tc.args.kind), tc.want)
		})
	}
}
