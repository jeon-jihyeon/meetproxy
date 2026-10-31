package main

import (
	"fmt"
	"io"

	"time"
)

type cli struct {
	data      string
	session   string
	now       time.Time
	in        io.Reader
	out       io.Writer
	slackBase string
	browser   browserScript
	// Shows meetproxy's own secret dialogs, none when nil
	dialog osDialog
	// The wall time a collection or a change ends by, zero for none
	deadline time.Time
}

func printVersion(c cli, _ []string, _ flags) (int, error) {
	_, err := fmt.Fprintln(c.out, version)
	return exitCode(err)
}

func printProtocol(c cli, _ []string, _ flags) (int, error) {
	_, err := fmt.Fprintln(c.out, protocol)
	return exitCode(err)
}
