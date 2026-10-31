package slackstate_test

import (
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
	"github.com/stretchr/testify/assert"
)

func TestTokenSnapshot(t *testing.T) {
	t.Parallel()
	type args struct {
		old     []byte
		current []byte
		exists  bool
	}
	type want struct{ current, replaced bool }
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"same bytes", args{[]byte("token-a\n"), []byte("token-a\n"), true}, want{true, false}},
		{"replacement", args{[]byte("token-a\n"), []byte("token-b\n"), true}, want{false, true}},
		{"exact snapshot", args{[]byte("token-a\n"), []byte("token-a"), true}, want{false, true}},
		{"removed", args{[]byte("token-a"), nil, false}, want{}},
		{"unobserved", args{nil, []byte("token-a"), true}, want{false, true}},
		{"empty observed", args{[]byte{}, []byte{}, true}, want{true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token := slackstate.RestoreToken(tc.args.old)
			assert.Equal(t, tc.want.current, token.Current(tc.args.current, tc.args.exists))
			assert.Equal(t, tc.want.replaced, token.Replaced(tc.args.current, tc.args.exists))
		})
	}
}

func TestTokenLogout(t *testing.T) {
	t.Parallel()
	type args struct {
		token             []byte
		replaced, revoked bool
		refusal           string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"no token", args{}, "no token"},
		{"replacement", args{[]byte("old"), true, true, "invalid_auth"},
			"not removed: another session stored a new token meanwhile"},
		{"revoked", args{[]byte("old"), false, true, "token_revoked"}, "revoked"},
		{"invalid auth", args{[]byte("old"), false, true, "invalid_auth"}, "revoked"},
		{"not authed", args{[]byte("old"), false, true, "not_authed"}, "revoked"},
		{"inactive", args{[]byte("old"), false, true, "account_inactive"}, "revoked"},
		{"unconfirmed revoke", args{[]byte("old"), false, false, "token_revoked"},
			"removed, not revoked: revoke the token under OAuth & Permissions of the app"},
		{"still live", args{[]byte("old"), false, true, ""},
			"removed, not revoked: revoke the token under OAuth & Permissions of the app"},
		{"other error", args{[]byte("old"), false, true, "ratelimited"},
			"removed, not revoked: revoke the token under OAuth & Permissions of the app"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := slackstate.RestoreToken(tc.args.token).Logout(tc.args.replaced, tc.args.revoked, tc.args.refusal)
			assert.Equal(t, tc.want, got)
		})
	}
}
