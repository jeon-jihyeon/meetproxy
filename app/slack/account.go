package slack

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jeon-jihyeon/meetproxy/internal/slackapi"
	"github.com/jeon-jihyeon/meetproxy/internal/slackstate"
	"github.com/jeon-jihyeon/meetproxy/internal/store"
)

var (
	ErrNoToken      = errors.New("no Slack token, run /meetproxy:slack to set one up")
	ErrInvalidToken = errors.New("stdin must hold one user token that starts with xoxp-")
)

type Config struct {
	Data     string
	Base     string
	Now      time.Time
	Deadline time.Time
}

type Account struct {
	files    store.Delivery
	base     string
	now      time.Time
	deadline time.Time
}

type Auth struct {
	User    string
	Team    string
	Host    string
	Scopes  []string
	Missing []string
}

func RequiredScopes() []string {
	return slackstate.RequiredScopes()
}

func New(c Config) *Account {
	return &Account{files: store.NewDelivery(c.Data), base: c.Base, now: c.Now, deadline: c.Deadline}
}

func (a *Account) StoreToken(input []byte) (Auth, error) {
	if len(input) > 4096 {
		return Auth{}, ErrInvalidToken
	}
	token := strings.TrimSpace(string(input))
	if !strings.HasPrefix(token, "xoxp-") || strings.ContainsAny(token, " \t\n") {
		return Auth{}, ErrInvalidToken
	}
	client, err := slackapi.New(a.base, token)
	if err != nil {
		return Auth{}, err
	}
	got, err := client.AuthTest()
	if err != nil {
		return Auth{}, err
	}
	unlock, err := a.files.LockToken()
	if err != nil {
		return Auth{}, err
	}
	defer unlock()
	if err := a.files.WriteToken([]byte(token + "\n")); err != nil {
		return Auth{}, err
	}
	err = a.files.WriteAuth(a.authRecord(got))
	return auth(got), err
}

func (a *Account) Auth(live bool) (Auth, error) {
	if !live {
		if got, found, err := a.files.Auth(); found && err == nil && got.User != "" {
			return auth(slackapi.Auth{User: got.User, Team: got.Team, Host: got.Host, Scopes: got.Scopes}), nil
		}
	}
	value, err := a.files.Token()
	if errors.Is(err, os.ErrNotExist) {
		return Auth{}, ErrNoToken
	}
	if err != nil {
		return Auth{}, err
	}
	client, err := slackapi.New(a.base, strings.TrimSpace(string(value)))
	if err != nil {
		return Auth{}, err
	}
	if !a.deadline.IsZero() {
		client = client.Until(a.deadline)
	}
	got, err := client.AuthTest()
	if err != nil {
		return Auth{}, err
	}
	return auth(got), a.saveAuth(value, got)
}

func (a *Account) StoredAuth() (Auth, bool, error) {
	got, found, err := a.files.Auth()
	if !found || err != nil {
		return Auth{}, found, err
	}
	return auth(slackapi.Auth{User: got.User, Team: got.Team, Host: got.Host, Scopes: got.Scopes}), true, nil
}

func (a *Account) saveAuth(token []byte, got slackapi.Auth) error {
	unlock, err := a.files.LockToken()
	if err != nil {
		return err
	}
	defer unlock()
	value, err := a.files.Token()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !slackstate.RestoreToken(token).Current(value, err == nil) {
		return nil
	}
	return a.files.WriteAuth(a.authRecord(got))
}

func (a *Account) authRecord(got slackapi.Auth) store.SlackAuth {
	return store.SlackAuth{User: got.User, Team: got.Team, Host: got.Host, Scopes: got.Scopes, At: a.now.UTC()}
}

func auth(got slackapi.Auth) Auth {
	return Auth{User: got.User, Team: got.Team, Host: got.Host, Scopes: got.Scopes,
		Missing: slackstate.RestoreScopes(got.Scopes).Missing()}
}
