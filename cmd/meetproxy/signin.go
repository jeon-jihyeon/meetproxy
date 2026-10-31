package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsignin "github.com/jeon-jihyeon/meetproxy/app/signin"
)

type browserScript struct {
	run   func(context.Context, string) (string, error)
	clock func() time.Time
	sleep func(time.Duration)
}

func (c cli) signin() *appsignin.Service {
	return appsignin.New(appsignin.Config{Data: c.data, Session: c.session, Now: c.now}, appsignin.Runtime{
		Run: c.browser.run, Clock: c.browser.clock, Sleep: c.browser.sleep, MarkClock: time.Now,
	})
}

func (c cli) signinOpen(log, offset, browser string) error {
	at, err := strconv.ParseInt(offset, 10, 64)
	if err != nil || at < 0 {
		return fmt.Errorf("%w: --offset takes the log's size in bytes", errUsage)
	}
	word, err := c.signin().Open(log, at, browser)
	if err != nil {
		return err
	}
	return c.print(word)
}

func (c cli) signinWait() error {
	word, err := c.signin().Wait()
	if err != nil {
		return err
	}
	return c.print(word)
}

func (c cli) print(word string) error {
	_, err := fmt.Fprintln(c.out, word)
	return err
}

func osBrowser() browserScript {
	runtime := appsignin.SystemRuntime()
	return browserScript{run: runtime.Run, clock: runtime.Clock, sleep: runtime.Sleep}
}
