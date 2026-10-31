package slack

import (
	"errors"
	"os"
	"strings"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
)

func (a *Account) Logout() (string, error) {
	value, err := a.files.Token()
	if errors.Is(err, os.ErrNotExist) {
		return slackstate.RestoreToken(nil).Logout(false, false, ""), nil
	}
	if err != nil {
		return "", err
	}
	token := slackstate.RestoreToken(value)
	client, err := slackapi.New(a.base, strings.TrimSpace(string(value)))
	if err != nil {
		return "", err
	}
	revoked, err := client.Revoke()
	revoked = revoked && err == nil
	replaced, err := a.removeToken(token)
	if err != nil {
		return "", err
	}
	if replaced {
		return token.Logout(true, revoked, ""), nil
	}
	_, err = client.AuthTest()
	var response *slackapi.Error
	code := ""
	if errors.As(err, &response) {
		code = response.Code
	}
	return token.Logout(false, revoked, code), nil
}

func (a *Account) removeToken(token slackstate.Token) (bool, error) {
	unlock, err := a.files.LockToken()
	if err != nil {
		return false, err
	}
	defer unlock()
	value, err := a.files.Token()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if token.Replaced(value, err == nil) {
		return true, nil
	}
	if err := a.files.RemoveToken(); err != nil {
		return false, err
	}
	return false, a.forgetFailure()
}
