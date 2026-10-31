package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	appslack "github.com/jeon-jihyeon/meetproxy/app/slack"
)

const maxTokenBytes = 4096

func (c cli) slackAccount() *appslack.Account {
	return appslack.New(appslack.Config{
		Data: c.data, Base: os.Getenv(slackAPIEnv), Now: c.now, Deadline: c.deadline,
	})
}

func (c cli) slackToken() error {
	b, err := io.ReadAll(io.LimitReader(c.in, maxTokenBytes+1))
	if err != nil {
		return err
	}
	a, err := c.slackAccount().StoreToken(b)
	if errors.Is(err, appslack.ErrInvalidToken) {
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]any{"user": a.User, "team": a.Team, "host": a.Host, "missing": a.Missing})
}

func (c cli) slackLogout() error {
	result, err := c.slackAccount().Logout()
	if err != nil {
		return err
	}
	return c.print(result)
}

func (c cli) slackWhoami() error {
	a, err := c.slackAccount().Auth(true)
	if err != nil {
		return err
	}
	return writeJSON(c.out, map[string]string{"user": a.User, "team": a.Team, "host": a.Host})
}
