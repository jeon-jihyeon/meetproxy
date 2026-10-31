package slackstate

import "slices"

type Token struct {
	value string
	known bool
}

func RestoreToken(value []byte) Token {
	return Token{value: string(value), known: value != nil}
}

func (t Token) Current(value []byte, exists bool) bool {
	return t.known && exists && t.value == string(value)
}

func (t Token) Replaced(value []byte, exists bool) bool {
	return exists && t.value != string(value)
}

func (t Token) Logout(replaced, revoked bool, refusal string) string {
	if !t.known {
		return "no token"
	}
	if replaced {
		return "not removed: another session stored a new token meanwhile"
	}
	if revoked && slices.Contains([]string{"invalid_auth", "token_revoked", "not_authed", "account_inactive"}, refusal) {
		return "revoked"
	}
	return "removed, not revoked: revoke the token under OAuth & Permissions of the app"
}
